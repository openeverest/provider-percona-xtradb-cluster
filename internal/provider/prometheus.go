// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"fmt"
	"maps"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/definition/components"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// monitorUser is the operator-managed MySQL user with the read-only
	// grants (SELECT, PROCESS, REPLICATION CLIENT) that exporters need.
	monitorUser = "monitor"

	mysqldExporterContainerName = "mysqld-exporter"
	mysqldExporterPortName      = "mysqld-exporter" // IANA port names allow at most 15 characters.
	mysqldExporterPort          = 9104
	mysqldExporterMetricsPath   = "/metrics"
)

func prometheusEnabled(params *components.MonitoringParameters) bool {
	return params.Prometheus != nil && params.Prometheus.Enabled
}

// applyPrometheusExporter adds a mysqld_exporter sidecar to every MySQL pod.
func applyPrometheusExporter(pxc *pxcv1.PerconaXtraDBCluster, params *components.MonitoringParameters, providerSpec *corev1alpha1.ProviderSpec) error {
	if !prometheusEnabled(params) {
		return nil
	}

	image := defaultImageForComponentType(providerSpec, common.ComponentTypeMySQLDExporter)
	if image == "" {
		return fmt.Errorf("cannot resolve image for component type %q", common.ComponentTypeMySQLDExporter)
	}

	pxc.Spec.PXC.Sidecars = append(pxc.Spec.PXC.Sidecars, mysqldExporterContainer(image, pxc.Spec.SecretsName))
	return nil
}

func mysqldExporterContainer(image, usersSecretName string) corev1.Container {
	return corev1.Container{
		Name:            mysqldExporterContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Args: []string{
			"--mysqld.address=127.0.0.1:3306",
			"--mysqld.username=" + monitorUser,
		},
		Env: []corev1.EnvVar{{
			Name: "MYSQLD_EXPORTER_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: usersSecretName},
					Key:                  monitorUser,
				},
			},
		}},
		Ports: []corev1.ContainerPort{{
			Name:          mysqldExporterPortName,
			ContainerPort: mysqldExporterPort,
			Protocol:      corev1.ProtocolTCP,
		}},
	}
}

// syncPodMonitor creates the PodMonitor that scrapes the mysqld_exporter
// sidecars, or removes it once the Prometheus integration is turned off.
func syncPodMonitor(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, params *components.MonitoringParameters) error {
	name := mysqldExporterPodMonitorName(c.Name())

	if !prometheusEnabled(params) {
		err := c.Delete(&monitoringv1.PodMonitor{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: c.Namespace()}})
		if err != nil && !meta.IsNoMatchError(err) {
			return fmt.Errorf("delete PodMonitor %q: %w", name, err)
		}
		return nil
	}

	podMonitor := mysqldExporterPodMonitor(c.ObjectMeta(name), naming.LabelsPXC(pxc), params.Prometheus.PodMonitorLabels)
	if err := c.Apply(podMonitor); err != nil {
		if meta.IsNoMatchError(err) {
			return fmt.Errorf("prometheus monitoring requires the Prometheus Operator PodMonitor CRD (monitoring.coreos.com/v1): %w", err)
		}
		return fmt.Errorf("apply PodMonitor %q: %w", name, err)
	}
	return nil
}

func mysqldExporterPodMonitorName(instanceName string) string {
	return instanceName + "-" + mysqldExporterContainerName
}

// mysqldExporterPodMonitor builds the PodMonitor. User labels never override
// the managed ones from objectMeta, which identify the owning Instance.
func mysqldExporterPodMonitor(objectMeta metav1.ObjectMeta, podSelector, userLabels map[string]string) *monitoringv1.PodMonitor {
	labels := maps.Clone(userLabels)
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, objectMeta.Labels)
	objectMeta.Labels = labels

	return &monitoringv1.PodMonitor{
		ObjectMeta: objectMeta,
		Spec: monitoringv1.PodMonitorSpec{
			Selector: metav1.LabelSelector{MatchLabels: podSelector},
			PodMetricsEndpoints: []monitoringv1.PodMetricsEndpoint{{
				Port: new(mysqldExporterPortName),
				Path: mysqldExporterMetricsPath,
			}},
		},
	}
}
