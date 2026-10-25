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
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"
)

const (
	monTestInstance    = "db"
	monTestNamespace   = "team-a"
	monTestUsersSecret = "everest-secrets-db"
	monTestPMMImage    = "percona/pmm-client:3.8.0"
)

var monTestProviderSpec = corev1alpha1.ProviderSpec{
	ComponentTypes: map[string]corev1alpha1.ComponentType{
		common.ComponentTypeMySQLDExporter: {
			DefaultVersion: "0.20.0",
			Versions:       []corev1alpha1.ComponentVersion{{Version: "0.20.0", Image: "prom/mysqld-exporter:v0.20.0"}},
		},
	},
	Monitoring: &corev1alpha1.ProviderMonitoring{
		Integrations: map[string]corev1alpha1.MonitoringIntegration{pmmIntegration: {AgentVersions: ">=3.0.0 <4.0.0"}},
	},
}

// monitoringContext returns the context of an Instance listing entries in
// spec.monitoring; objs are what the cluster holds besides the Instance and
// its Provider.
func monitoringContext(t *testing.T, entries []corev1alpha1.InstanceMonitoringDestination, objs ...client.Object) *controller.Context {
	t.Helper()
	return monitoringContextIntercepted(t, entries, interceptor.Funcs{}, objs...)
}

// monitoringContextIntercepted is monitoringContext with a client whose calls
// go through funcs.
func monitoringContextIntercepted(t *testing.T, entries []corev1alpha1.InstanceMonitoringDestination, funcs interceptor.Funcs, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, monitoringv1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: monTestInstance, Namespace: monTestNamespace, UID: "instance-uid"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{
				common.ComponentEngine: {Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				}},
			},
		},
	}
	if entries != nil {
		in.Spec.Monitoring = &corev1alpha1.InstanceMonitoringSpec{Destinations: entries}
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "percona-xtradb-cluster"},
		Spec:       monTestProviderSpec,
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in, provider)...).WithInterceptorFuncs(funcs).Build()
	return controller.NewContext(context.Background(), cl, in, provider.Name)
}

func monEntry(name, destination string) corev1alpha1.InstanceMonitoringDestination {
	return corev1alpha1.InstanceMonitoringDestination{
		Name:           name,
		DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
	}
}

func monBindingName(entry string) string {
	return monTestInstance + "-" + entry
}

// monBinding is the binding core materialises for an entry, Accepted for its
// generation when accepted is set.
func monBinding(entry, destination, class string, mode corev1alpha1.MonitoringExecutionMode, accepted bool, parameters string) *monitoringv1alpha1.MonitoringBinding {
	b := &monitoringv1alpha1.MonitoringBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:       monBindingName(entry),
			Namespace:  monTestNamespace,
			Generation: 1,
			Labels: map[string]string{
				monitoringv1alpha1.BindingInstanceLabel: monTestInstance,
				monitoringv1alpha1.BindingEntryLabel:    entry,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: corev1alpha1.GroupVersion.String(),
				Kind:       "Instance",
				Name:       monTestInstance,
				UID:        "instance-uid",
				Controller: new(true),
			}},
		},
		Spec: monitoringv1alpha1.MonitoringBindingSpec{
			InstanceRef:    commonv1alpha1.ObjectRef{Name: monTestInstance},
			DestinationRef: commonv1alpha1.MonitoringDestinationRef{Name: destination},
			ClassRef:       &commonv1alpha1.ObjectRef{Name: class},
			ExecutionMode:  mode,
		},
	}
	if parameters != "" {
		b.Spec.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}
	if accepted {
		b.Status.Conditions = []metav1.Condition{{
			Type:               monitoringv1alpha1.MonitoringBindingConditionAccepted,
			Status:             metav1.ConditionTrue,
			Reason:             monitoringv1alpha1.ReasonAccepted,
			ObservedGeneration: 1,
		}}
	}
	return b
}

func monPMMBinding(accepted bool, parameters string) *monitoringv1alpha1.MonitoringBinding {
	return monBinding("pmm", "pmm-prod", "pmm", corev1alpha1.MonitoringExecutionModeProviderManaged, accepted, parameters)
}

func monPrometheusBinding() *monitoringv1alpha1.MonitoringBinding {
	return monBinding("prometheus", "prometheus", "prometheus", corev1alpha1.MonitoringExecutionModeExtensionManaged, true, "")
}

func monPMMClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "pmm"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: "openeverest.io/monitoring-pmm",
			ProviderManaged: &monitoringv1alpha1.ProviderManagedFulfilment{
				Integration:  pmmIntegration,
				AgentImage:   monTestPMMImage,
				AgentVersion: "3.8.0",
			},
		},
	}
}

func monPrometheusClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "prometheus"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ControllerName: "openeverest.io/monitoring-prometheus",
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires: monitoringv1alpha1.SourceRequirements{Metrics: true},
			},
		},
	}
}

func monDestination(name, class, parameters, credentialsSecret string) *monitoringv1alpha1.MonitoringDestination {
	d := &monitoringv1alpha1.MonitoringDestination{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: monTestNamespace},
		Spec:       monitoringv1alpha1.MonitoringDestinationSpec{ClassRef: commonv1alpha1.ObjectRef{Name: class}},
	}
	if parameters != "" {
		d.Spec.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}
	if credentialsSecret != "" {
		d.Spec.CredentialsSecretRef = &commonv1alpha1.SecretRef{Name: credentialsSecret}
	}
	return d
}

func monPMMDestination() *monitoringv1alpha1.MonitoringDestination {
	return monDestination("pmm-prod", "pmm", `{"url":"https://pmm.example.com:8443/graph"}`, "pmm-prod-credentials")
}

func monSecret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: monTestNamespace},
		Data:       map[string][]byte{},
	}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func monPMMCredentials() *corev1.Secret {
	return monSecret("pmm-prod-credentials", map[string]string{pmmAPIKeyKey: "glsa_token"})
}

// monLivePXC is the engine CR as the operator currently holds it, with every
// pod ready when state is Ready.
func monLivePXC(state pxcv1.AppState, proxyType string, exporter, pmm bool) *pxcv1.PerconaXtraDBCluster {
	pxc := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: monTestInstance, Namespace: monTestNamespace},
		Spec: pxcv1.PerconaXtraDBClusterSpec{
			SecretsName: monTestUsersSecret,
			PXC:         &pxcv1.PXCSpec{PodSpec: &pxcv1.PodSpec{Enabled: true}},
		},
		Status: pxcv1.PerconaXtraDBClusterStatus{Status: state},
	}
	switch proxyType {
	case common.ProxyTypeHAProxy:
		pxc.Spec.HAProxy = &pxcv1.HAProxySpec{PodSpec: pxcv1.PodSpec{Enabled: true}}
	case common.ProxyTypeProxySQL:
		pxc.Spec.ProxySQL = &pxcv1.ProxySQLSpec{PodSpec: pxcv1.PodSpec{Enabled: true}}
	}
	if state == pxcv1.AppStateReady {
		monReadyPods(pxc, 3, 2)
	}
	if exporter {
		pxc.Spec.PXC.Sidecars = []corev1.Container{{Name: mysqldExporterContainerName}}
	}
	if pmm {
		pxc.Spec.PMM = &pxcv1.PMMSpec{Enabled: true, ServerHost: "live-pmm.example.com", Image: "percona/pmm-client:3.7.0"}
	}
	return pxc
}

// monReadyPods sets how many engine and proxy pods the operator reports
// ready.
func monReadyPods(pxc *pxcv1.PerconaXtraDBCluster, engine, proxy int32) *pxcv1.PerconaXtraDBCluster {
	pxc.Status.PXC.Ready = engine
	switch {
	case pxc.Spec.HAProxyEnabled():
		pxc.Status.HAProxy.Ready = proxy
	case pxc.Spec.ProxySQLEnabled():
		pxc.Status.ProxySQL.Ready = proxy
	}
	return pxc
}

// monDesiredPXC is the engine CR Sync is building, before monitoring.
func monDesiredPXC() *pxcv1.PerconaXtraDBCluster {
	spec := defaultSpec()
	spec.SecretsName = monTestUsersSecret
	return &pxcv1.PerconaXtraDBCluster{Spec: spec}
}

func TestMonitoringSources(t *testing.T) {
	t.Parallel()

	engine := corev1alpha1.MetricsEndpoint{
		Component: common.ComponentEngine,
		Kind:      "mysql",
		PodSelector: map[string]string{
			controller.InstanceLabel:  monTestInstance,
			controller.ComponentLabel: common.ComponentEngine,
		},
		Port:   intstr.FromString("metrics"),
		Path:   "/metrics",
		Scheme: "http",
	}
	haproxy := corev1alpha1.MetricsEndpoint{
		Component: common.ComponentProxy,
		Kind:      "haproxy",
		PodSelector: map[string]string{
			controller.InstanceLabel:  monTestInstance,
			controller.ComponentLabel: common.ComponentProxy,
		},
		Port:   intstr.FromInt32(8404),
		Path:   "/metrics",
		Scheme: "http",
	}

	tests := []struct {
		name string
		objs []client.Object
		want []corev1alpha1.MetricsEndpoint
	}{
		{
			name: "HAProxy and the exporter sidecar",
			objs: []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, true, false)},
			want: []corev1alpha1.MetricsEndpoint{engine, haproxy},
		},
		{
			name: "ProxySQL publishes no proxy endpoint",
			objs: []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeProxySQL, true, false)},
			want: []corev1alpha1.MetricsEndpoint{engine},
		},
		{
			name: "no engine endpoint without the sidecar",
			objs: []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true)},
			want: []corev1alpha1.MetricsEndpoint{haproxy},
		},
		{
			name: "a rolling restart keeps the endpoints",
			objs: []client.Object{monReadyPods(monLivePXC(pxcv1.AppStateInit, common.ProxyTypeHAProxy, true, false), 2, 1)},
			want: []corev1alpha1.MetricsEndpoint{engine, haproxy},
		},
		{
			name: "each component once one of its pods is ready",
			objs: []client.Object{monReadyPods(monLivePXC(pxcv1.AppStateInit, common.ProxyTypeHAProxy, true, false), 0, 1)},
			want: []corev1alpha1.MetricsEndpoint{haproxy},
		},
		{
			name: "nothing before a pod is ready",
			objs: []client.Object{monLivePXC(pxcv1.AppStateInit, common.ProxyTypeHAProxy, true, false)},
		},
		{
			name: "nothing before the cluster exists",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, nil, tt.objs...)

			sources, err := (&PXCProvider{}).MonitoringSources(c)
			require.NoError(t, err)
			assert.Equal(t, tt.want, sources.Metrics)
		})
	}
}

func TestMonitoringIntegrations(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "definition", "monitoring.yaml"))
	require.NoError(t, err)
	definition := corev1alpha1.ProviderMonitoring{}
	require.NoError(t, yaml.Unmarshal(raw, &definition))

	assert.ElementsMatch(t, slices.Collect(maps.Keys(definition.Integrations)), (&PXCProvider{}).MonitoringIntegrations())
}

func TestMetricsExporterFollowsRequiredSources(t *testing.T) {
	t.Parallel()

	prometheusEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("prometheus", "prometheus")}
	pmmEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}

	tests := []struct {
		name    string
		entries []corev1alpha1.InstanceMonitoringDestination
		objs    []client.Object
		want    bool
	}{
		{
			name:    "a bound class requires metrics",
			entries: prometheusEntry,
			objs:    []client.Object{monPrometheusBinding(), monDestination("prometheus", "prometheus", "", ""), monPrometheusClass()},
			want:    true,
		},
		{
			name:    "only PMM is bound",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, true, true),
			},
			want: false,
		},
		{
			name: "no destinations",
			objs: []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, true, false)},
			want: false,
		},
		{
			name:    "an entry without a binding keeps the live sidecar",
			entries: prometheusEntry,
			objs:    []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, true, false)},
			want:    true,
		},
		{
			name:    "an entry without a binding adds none",
			entries: prometheusEntry,
			objs:    []client.Object{monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false)},
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, tt.entries, tt.objs...)
			pxc := monDesiredPXC()

			require.NoError(t, applyMonitoring(c, pxc, &monTestProviderSpec))
			assert.Equal(t, tt.want, hasMetricsExporter(pxc))
		})
	}
}

func TestMetricsExporterSidecar(t *testing.T) {
	t.Parallel()

	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("prometheus", "prometheus")},
		monPrometheusBinding(), monDestination("prometheus", "prometheus", "", ""), monPrometheusClass())
	pxc := monDesiredPXC()

	require.NoError(t, applyMonitoring(c, pxc, &monTestProviderSpec))

	require.Len(t, pxc.Spec.PXC.Sidecars, 1)
	sidecar := pxc.Spec.PXC.Sidecars[0]
	assert.Equal(t, "prom/mysqld-exporter:v0.20.0", sidecar.Image, "the image comes from the provider's version catalog")
	assert.Equal(t, []string{"--mysqld.address=127.0.0.1:3306", "--mysqld.username=monitor"}, sidecar.Args)
	require.Len(t, sidecar.Env, 1)
	assert.Equal(t, &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: monTestUsersSecret},
		Key:                  "monitor",
	}, sidecar.Env[0].ValueFrom.SecretKeyRef, "the operator's monitor user password")
	assert.Equal(t, []corev1.ContainerPort{{Name: "metrics", ContainerPort: 9104, Protocol: corev1.ProtocolTCP}}, sidecar.Ports)
}

func TestPMMFollowsRequiredSources(t *testing.T) {
	t.Parallel()

	pmmEntry := []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}
	livePMM := monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true).Spec.PMM

	tests := []struct {
		name       string
		entries    []corev1alpha1.InstanceMonitoringDestination
		objs       []client.Object
		wantHost   string
		wantLive   bool
		wantNoPMM  bool
		wantToken  string
		wantMirror bool
		wantResult monitoringv1alpha1.ConfiguredReason
	}{
		{
			name:    "an Accepted binding is rendered",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, nil),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantHost:   "pmm.example.com:8443",
			wantToken:  "glsa_token",
			wantMirror: true,
		},
		{
			name:    "a binding that is not Accepted keeps the live spec.pmm",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(false, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantLive:   true,
			wantToken:  "old_token",
			wantMirror: true,
		},
		{
			name:    "a deleted destination keeps the live spec.pmm",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMClass(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantLive:   true,
			wantToken:  "old_token",
			wantMirror: true,
		},
		{
			name:    "an entry without a binding keeps the live spec.pmm",
			entries: pmmEntry,
			objs: []client.Object{
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantLive:   true,
			wantToken:  "old_token",
			wantMirror: true,
		},
		{
			name:    "an entry without a binding enables nothing",
			entries: pmmEntry,
			objs: []client.Object{
				monSecret(monTestUsersSecret, nil),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false),
			},
			wantNoPMM: true,
		},
		{
			name: "a removed entry disables PMM and drops the token mirror",
			objs: []client.Object{
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token", "root": "pw"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantNoPMM: true,
		},
		{
			name: "a removed entry disables PMM while its binding remains",
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			},
			wantNoPMM: true,
		},
		{
			name:    "a usable binding is rendered next to an entry without a binding",
			entries: []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod"), monEntry("prometheus", "prometheus")},
			objs: []client.Object{
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, nil),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false),
			},
			wantHost:   "pmm.example.com:8443",
			wantToken:  "glsa_token",
			wantMirror: true,
		},
		{
			name:    "a binding that cannot be rendered enables nothing before the cluster exists",
			entries: pmmEntry,
			objs: []client.Object{
				monPMMBinding(true, ""), monDestination("pmm-prod", "pmm", `{}`, "pmm-prod-credentials"), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, nil),
			},
			wantNoPMM:  true,
			wantResult: monitoringv1alpha1.ReasonRenderFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, tt.entries, tt.objs...)
			pxc := monDesiredPXC()

			require.NoError(t, applyMonitoring(c, pxc, &monTestProviderSpec))

			switch {
			case tt.wantNoPMM:
				assert.Nil(t, pxc.Spec.PMM)
			case tt.wantLive:
				assert.Equal(t, livePMM, pxc.Spec.PMM)
			default:
				require.NotNil(t, pxc.Spec.PMM)
				assert.True(t, pxc.Spec.PMM.Enabled)
				assert.Equal(t, tt.wantHost, pxc.Spec.PMM.ServerHost)
			}

			users := &corev1.Secret{}
			require.NoError(t, c.Get(users, monTestUsersSecret))
			token, mirrored := users.Data[pmmServerTokenKey]
			assert.Equal(t, tt.wantMirror, mirrored)
			assert.Equal(t, tt.wantToken, string(token))
			reason, _, _ := c.MonitoringBindingResult(monBindingName("pmm"))
			assert.Equal(t, tt.wantResult, reason)
		})
	}
}

func TestPMMRender(t *testing.T) {
	t.Parallel()

	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")},
		monPMMBinding(true, `{"resources":{"requests":{"cpu":"500m"},"limits":{"memory":"512Mi"}}}`),
		monPMMDestination(), monPMMClass(), monPMMCredentials(),
		monSecret(monTestUsersSecret, map[string]string{"root": "pw"}),
	)
	pxc := monDesiredPXC()

	require.NoError(t, applyMonitoring(c, pxc, &monTestProviderSpec))

	assert.Equal(t, &pxcv1.PMMSpec{
		Enabled:           true,
		ServerHost:        "pmm.example.com:8443",
		Image:             monTestPMMImage,
		CustomClusterName: monTestInstance,
		ImagePullPolicy:   corev1.PullIfNotPresent,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("99604Ki"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
		},
		PxcParams:      "--custom-labels=namespace=team-a,core_openeverest_io_instance=db",
		ProxysqlParams: "--custom-labels=namespace=team-a,core_openeverest_io_instance=db,core_openeverest_io_component=proxy",
	}, pxc.Spec.PMM)

	users := &corev1.Secret{}
	require.NoError(t, c.Get(users, monTestUsersSecret))
	assert.Equal(t, "glsa_token", string(users.Data[pmmServerTokenKey]))
	assert.Equal(t, "pw", string(users.Data["root"]), "the operator's users are left alone")
}

func TestPMMRenderFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		binding     *monitoringv1alpha1.MonitoringBinding
		destination *monitoringv1alpha1.MonitoringDestination
		credentials *corev1.Secret
		wantMessage string
	}{
		{
			name:        "destination parameters that do not decode",
			binding:     monPMMBinding(true, ""),
			destination: monDestination("pmm-prod", "pmm", `{"url":443}`, "pmm-prod-credentials"),
			credentials: monPMMCredentials(),
			wantMessage: "decode MonitoringDestination pmm-prod parameters",
		},
		{
			name:        "destination without url",
			binding:     monPMMBinding(true, ""),
			destination: monDestination("pmm-prod", "pmm", `{}`, "pmm-prod-credentials"),
			credentials: monPMMCredentials(),
			wantMessage: "MonitoringDestination pmm-prod parameters.url: url is empty",
		},
		{
			name:        "destination url without host",
			binding:     monPMMBinding(true, ""),
			destination: monDestination("pmm-prod", "pmm", `{"url":"https://"}`, "pmm-prod-credentials"),
			credentials: monPMMCredentials(),
			wantMessage: "MonitoringDestination pmm-prod parameters.url: missing host",
		},
		{
			name:        "destination without credentials",
			binding:     monPMMBinding(true, ""),
			destination: monDestination("pmm-prod", "pmm", `{"url":"https://pmm.example.com"}`, ""),
			wantMessage: "MonitoringDestination pmm-prod has no credentialsSecretRef",
		},
		{
			name:        "missing credentials Secret",
			binding:     monPMMBinding(true, ""),
			destination: monPMMDestination(),
			wantMessage: "credentials Secret pmm-prod-credentials of MonitoringDestination pmm-prod not found",
		},
		{
			name:        "credentials Secret without apiKey",
			binding:     monPMMBinding(true, ""),
			destination: monPMMDestination(),
			credentials: monSecret("pmm-prod-credentials", map[string]string{"token": "x"}),
			wantMessage: "credentials Secret pmm-prod-credentials has no apiKey",
		},
		{
			name:        "entry resources that do not decode",
			binding:     monPMMBinding(true, `{"resources":{"limits":"lots"}}`),
			destination: monPMMDestination(),
			credentials: monPMMCredentials(),
			wantMessage: "decode parameters of destination entry pmm",
		},
		{
			name:        "entry request above its own limit",
			binding:     monPMMBinding(true, `{"resources":{"requests":{"cpu":"2"},"limits":{"cpu":"1"}}}`),
			destination: monPMMDestination(),
			credentials: monPMMCredentials(),
			wantMessage: "parameters of destination entry pmm: resources.requests.cpu (2) is above resources.limits.cpu (1)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := []client.Object{
				tt.binding, tt.destination, monPMMClass(),
				monSecret(monTestUsersSecret, map[string]string{pmmServerTokenKey: "old_token"}),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			}
			if tt.credentials != nil {
				objs = append(objs, tt.credentials)
			}
			c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}, objs...)
			pxc := monDesiredPXC()

			require.NoError(t, applyMonitoring(c, pxc, &monTestProviderSpec), "a bad binding never fails Sync")

			reason, message, staged := c.MonitoringBindingResult(monBindingName("pmm"))
			require.True(t, staged)
			assert.Equal(t, monitoringv1alpha1.ReasonRenderFailed, reason)
			assert.Contains(t, message, tt.wantMessage)
			require.NotNil(t, pxc.Spec.PMM)
			assert.Equal(t, "live-pmm.example.com", pxc.Spec.PMM.ServerHost, "the live spec.pmm is kept")

			users := &corev1.Secret{}
			require.NoError(t, c.Get(users, monTestUsersSecret))
			assert.Equal(t, "old_token", string(users.Data[pmmServerTokenKey]))
		})
	}
}

func TestPMMAPIErrorsFailSync(t *testing.T) {
	t.Parallel()

	unavailable := apierrors.NewServiceUnavailable("etcd is unavailable")
	tests := []struct {
		name  string
		funcs interceptor.Funcs
	}{
		{
			name: "reading the credentials Secret",
			funcs: interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if key.Name == "pmm-prod-credentials" {
					return unavailable
				}
				return cl.Get(ctx, key, obj, opts...)
			}},
		},
		{
			name: "mirroring the token",
			funcs: interceptor.Funcs{Patch: func(context.Context, client.WithWatch, client.Object, client.Patch, ...client.PatchOption) error {
				return unavailable
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContextIntercepted(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("pmm", "pmm-prod")}, tt.funcs,
				monPMMBinding(true, ""), monPMMDestination(), monPMMClass(), monPMMCredentials(),
				monSecret(monTestUsersSecret, nil),
				monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, true),
			)

			require.ErrorIs(t, applyMonitoring(c, monDesiredPXC(), &monTestProviderSpec), unavailable)
			_, _, staged := c.MonitoringBindingResult(monBindingName("pmm"))
			assert.False(t, staged, "an API error is retried, not reported on the binding")
		})
	}
}

func TestPMMResources(t *testing.T) {
	t.Parallel()

	engine := func(memory string) *corev1.ResourceRequirements {
		return &corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(memory)}}
	}

	tests := []struct {
		name    string
		engine  *corev1.ResourceRequirements
		entry   *corev1.ResourceRequirements
		want    corev1.ResourceRequirements
		wantErr string
	}{
		{
			name:   "small engine",
			engine: engine("2Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsSmall},
		},
		{
			name:   "medium engine",
			engine: engine("8Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsMedium},
		},
		{
			name:   "large engine",
			engine: engine("64Gi"),
			want:   corev1.ResourceRequirements{Requests: pmmRequestsLarge},
		},
		{
			name: "no engine resources",
			want: corev1.ResourceRequirements{Requests: pmmRequestsSmall},
		},
		{
			name:   "entry overrides one request and sets limits",
			engine: engine("64Gi"),
			entry: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("228m"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
				Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			},
		},
		{
			name:   "entry limits below the sized requests lower them",
			engine: engine("64Gi"),
			entry: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		},
		{
			name:   "entry request below its own limit",
			engine: engine("64Gi"),
			entry: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
			want: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("228m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
			},
		},
		{
			name:   "entry request above its own limit",
			engine: engine("2Gi"),
			entry: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
			},
			wantErr: "resources.requests.memory (1Gi) is above resources.limits.memory (512Mi)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := pmmResources(tt.engine, tt.entry)
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
