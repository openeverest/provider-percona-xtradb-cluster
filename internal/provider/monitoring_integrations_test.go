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
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/provider-percona-xtradb-cluster/definition/components"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newPXCForMonitoringTest() *pxcv1.PerconaXtraDBCluster {
	return &pxcv1.PerconaXtraDBCluster{
		Spec: pxcv1.PerconaXtraDBClusterSpec{
			SecretsName: "everest-secrets-db",
			PXC:         &pxcv1.PXCSpec{PodSpec: &pxcv1.PodSpec{}},
		},
	}
}

func TestApplyPrometheusExporter(t *testing.T) {
	t.Parallel()

	providerSpec := &corev1alpha1.ProviderSpec{
		ComponentTypes: map[string]corev1alpha1.ComponentType{
			common.ComponentTypeMySQLDExporter: {
				DefaultVersion: "0.20.0",
				Versions: []corev1alpha1.ComponentVersion{
					{Version: "0.20.0", Image: "prom/mysqld-exporter:v0.20.0"},
				},
			},
		},
	}
	enabled := &components.MonitoringParameters{Prometheus: &components.PrometheusParameters{Enabled: true}}

	t.Run("disabled adds no sidecar", func(t *testing.T) {
		t.Parallel()
		pxc := newPXCForMonitoringTest()
		params := &components.MonitoringParameters{Prometheus: &components.PrometheusParameters{Enabled: false}}
		require.NoError(t, applyPrometheusExporter(pxc, params, providerSpec))
		assert.Empty(t, pxc.Spec.PXC.Sidecars)
	})

	t.Run("enabled adds exporter reading the monitor password from the users secret", func(t *testing.T) {
		t.Parallel()
		pxc := newPXCForMonitoringTest()
		require.NoError(t, applyPrometheusExporter(pxc, enabled, providerSpec))
		require.Len(t, pxc.Spec.PXC.Sidecars, 1)

		sidecar := pxc.Spec.PXC.Sidecars[0]
		assert.Equal(t, "prom/mysqld-exporter:v0.20.0", sidecar.Image)
		require.Len(t, sidecar.Env, 1)
		secretRef := sidecar.Env[0].ValueFrom.SecretKeyRef
		require.NotNil(t, secretRef)
		assert.Equal(t, "everest-secrets-db", secretRef.Name)
		assert.Equal(t, monitorUser, secretRef.Key)
		require.Len(t, sidecar.Ports, 1)
		assert.Equal(t, mysqldExporterPortName, sidecar.Ports[0].Name)
		assert.LessOrEqual(t, len(sidecar.Ports[0].Name), 15)
	})

	t.Run("missing catalog entry is an error", func(t *testing.T) {
		t.Parallel()
		pxc := newPXCForMonitoringTest()
		require.Error(t, applyPrometheusExporter(pxc, enabled, &corev1alpha1.ProviderSpec{}))
	})
}

func TestMySQLDExporterPodMonitorKeepsManagedLabels(t *testing.T) {
	t.Parallel()

	objectMeta := metav1.ObjectMeta{
		Name:      "db-mysqld-exporter",
		Namespace: "ns",
		Labels:    map[string]string{"app.kubernetes.io/instance": "db"},
	}
	userLabels := map[string]string{
		"release":                    "kube-prometheus-stack",
		"app.kubernetes.io/instance": "spoofed",
	}
	selector := map[string]string{"app.kubernetes.io/component": "pxc"}

	pm := mysqldExporterPodMonitor(objectMeta, selector, userLabels)

	assert.Equal(t, "kube-prometheus-stack", pm.Labels["release"])
	assert.Equal(t, "db", pm.Labels["app.kubernetes.io/instance"])
	assert.Equal(t, "spoofed", userLabels["app.kubernetes.io/instance"], "caller labels must not be mutated")
	assert.Equal(t, selector, pm.Spec.Selector.MatchLabels)
	require.Len(t, pm.Spec.PodMetricsEndpoints, 1)
	require.NotNil(t, pm.Spec.PodMetricsEndpoints[0].Port)
	assert.Equal(t, mysqldExporterPortName, *pm.Spec.PodMetricsEndpoints[0].Port)
}

func TestApplyCorootAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("disabled leaves annotations untouched", func(t *testing.T) {
		t.Parallel()
		pxc := newPXCForMonitoringTest()
		applyCorootAnnotations(pxc, &components.MonitoringParameters{})
		assert.Nil(t, pxc.Spec.PXC.Annotations)
	})

	t.Run("enabled references the users secret and keeps existing annotations", func(t *testing.T) {
		t.Parallel()
		pxc := newPXCForMonitoringTest()
		pxc.Spec.PXC.Annotations = map[string]string{"existing": "value"}
		params := &components.MonitoringParameters{Coroot: &components.CorootParameters{Enabled: true}}

		applyCorootAnnotations(pxc, params)

		annotations := pxc.Spec.PXC.Annotations
		assert.Equal(t, "value", annotations["existing"])
		assert.Equal(t, "true", annotations["coroot.com/mysql-scrape"])
		assert.Equal(t, monitorUser, annotations["coroot.com/mysql-scrape-credentials-username"])
		assert.Equal(t, "everest-secrets-db", annotations["coroot.com/mysql-scrape-credentials-secret-name"])
		assert.Equal(t, monitorUser, annotations["coroot.com/mysql-scrape-credentials-secret-password-key"])
		assert.NotContains(t, annotations, "coroot.com/mysql-scrape-credentials-password")
	})
}
