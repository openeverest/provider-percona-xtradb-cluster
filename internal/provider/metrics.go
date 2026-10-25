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
	"slices"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
)

const (
	// monitorUser is the operator-managed MySQL user with the read-only
	// grants (SELECT, PROCESS, REPLICATION CLIENT) that exporters need.
	monitorUser = "monitor"

	mysqldExporterContainerName = "mysqld-exporter"
	mysqldExporterPort          = 9104
	metricsPortName             = "metrics"
	metricsPath                 = "/metrics"
)

// applyMetricsExporter adds a mysqld_exporter sidecar to every engine pod
// while a class bound to the Instance requires metrics. While that is
// unresolved the sidecar stays as live, so the pods do not roll twice.
func applyMetricsExporter(pxc, live *pxcv1.PerconaXtraDBCluster, required controller.Requirement, providerSpec *corev1alpha1.ProviderSpec) error {
	if !required.Else(hasMetricsExporter(live)) {
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
			Name:          metricsPortName,
			ContainerPort: mysqldExporterPort,
			Protocol:      corev1.ProtocolTCP,
		}},
	}
}

// hasMetricsExporter reports whether the engine CR, nil before it exists,
// carries the exporter sidecar.
func hasMetricsExporter(pxc *pxcv1.PerconaXtraDBCluster) bool {
	if pxc == nil || pxc.Spec.PXC == nil {
		return false
	}
	return slices.ContainsFunc(pxc.Spec.PXC.Sidecars, func(c corev1.Container) bool {
		return c.Name == mysqldExporterContainerName
	})
}
