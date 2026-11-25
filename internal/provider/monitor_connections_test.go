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

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

func monConnectionClass() *monitoringv1alpha1.MonitoringClass {
	return &monitoringv1alpha1.MonitoringClass{
		ObjectMeta: metav1.ObjectMeta{Name: "releem"},
		Spec: monitoringv1alpha1.MonitoringClassSpec{
			ExtensionManaged: &monitoringv1alpha1.ExtensionManagedFulfilment{
				Requires: monitoringv1alpha1.SourceRequirements{Connection: true},
			},
			AuthModes: []commonv1alpha1.AuthMode{{Name: "APIKey"}},
		},
	}
}

// connectionObjects are what an Instance sending to a releem destination
// resolves through, plus objs.
func connectionObjects(objs ...client.Object) []client.Object {
	return append(objs,
		monConnectionClass(),
		monDestination("releem", "releem", "", commonv1alpha1.Authentication{Mode: "APIKey"}),
		monBinding("releem", "releem", "releem", corev1alpha1.MonitoringExecutionModeExtensionManaged, true, ""),
	)
}

func usersSecret(monitorPassword string) *corev1.Secret {
	return monSecret(monTestUsersSecret, map[string]string{"root": "root-pass", operatorMonitorUser: monitorPassword})
}

func mirrorSecret(t *testing.T, c *controller.Context) (*corev1.Secret, bool) {
	t.Helper()
	s := &corev1.Secret{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: monTestNamespace, Name: "db-monitoring-monitor"}, s)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	require.NoError(t, err)
	return s, true
}

func TestSyncMonitorCredentialMirrorsTheOperatorMonitorUser(t *testing.T) {
	t.Parallel()
	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("releem", "releem")},
		connectionObjects(usersSecret("monitor-pass"))...)

	require.NoError(t, syncMonitorCredential(c, monTestUsersSecret))

	s, ok := mirrorSecret(t, c)
	require.True(t, ok)
	assert.Equal(t, corev1.SecretTypeBasicAuth, s.Type)
	assert.Equal(t, "monitor", string(s.Data[corev1alpha1.CredentialSecretKeyUsername]))
	assert.Equal(t, "monitor-pass", string(s.Data[corev1alpha1.CredentialSecretKeyPassword]))
	assert.NotContains(t, s.Data, "root", "only the monitor user leaves the users Secret")
	assert.True(t, metav1.IsControlledBy(s, c.Instance()), "the runtime publishes only Instance-owned credentials")
}

func TestSyncMonitorCredentialFollowsARotatedPassword(t *testing.T) {
	t.Parallel()
	stale := monSecret("db-monitoring-monitor", map[string]string{"username": "monitor", "password": "old"})
	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("releem", "releem")},
		connectionObjects(usersSecret("new"), stale)...)

	require.NoError(t, syncMonitorCredential(c, monTestUsersSecret))

	s, ok := mirrorSecret(t, c)
	require.True(t, ok)
	assert.Equal(t, "new", string(s.Data[corev1alpha1.CredentialSecretKeyPassword]))
}

func TestSyncMonitorCredentialWaitsForTheUsersSecret(t *testing.T) {
	t.Parallel()
	c := monitoringContext(t, []corev1alpha1.InstanceMonitoringDestination{monEntry("releem", "releem")}, connectionObjects()...)

	require.NoError(t, syncMonitorCredential(c, monTestUsersSecret))

	_, ok := mirrorSecret(t, c)
	assert.False(t, ok)
}

func TestSyncMonitorCredentialDeletesTheMirrorOnceNoClassRequiresIt(t *testing.T) {
	t.Parallel()
	mirror := monSecret("db-monitoring-monitor", map[string]string{"username": "monitor", "password": "p"})
	c := monitoringContext(t, nil, usersSecret("p"), mirror)

	require.NoError(t, syncMonitorCredential(c, monTestUsersSecret))

	_, ok := mirrorSecret(t, c)
	assert.False(t, ok)
}

func TestMonitorConnectionSources(t *testing.T) {
	t.Parallel()
	mirror := monSecret("db-monitoring-monitor", map[string]string{"username": "monitor", "password": "p"})

	tests := []struct {
		name       string
		parameters string
		pxc        *pxcv1.PerconaXtraDBCluster
		objs       []client.Object
		wantTLS    *corev1alpha1.ConnectionTLS
		wantNone   bool
	}{
		{
			name: "TLS accepted by default", pxc: monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false), objs: []client.Object{mirror},
			wantTLS: &corev1alpha1.ConnectionTLS{CA: &commonv1alpha1.CABundleSource{SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: "db-ssl", Key: "ca.crt"}}},
		},
		{
			name: "TLS required", parameters: `{"tls":{"mode":"requireTLS"}}`,
			pxc: monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false), objs: []client.Object{mirror},
			wantTLS: &corev1alpha1.ConnectionTLS{Required: true, CA: &commonv1alpha1.CABundleSource{SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: "db-ssl", Key: "ca.crt"}}},
		},
		{name: "no mirror", pxc: monLivePXC(pxcv1.AppStateReady, common.ProxyTypeHAProxy, false, false), wantNone: true},
		{name: "no ready node", pxc: monLivePXC(pxcv1.AppStateInit, common.ProxyTypeHAProxy, false, false), objs: []client.Object{mirror}, wantNone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := monitoringContext(t, nil, tt.objs...)
			if tt.parameters != "" {
				c.Instance().Spec.Parameters = &runtime.RawExtension{Raw: []byte(tt.parameters)}
			}

			credentials, connections, err := monitorConnectionSources(c, tt.pxc)
			require.NoError(t, err)

			if tt.wantNone {
				assert.Empty(t, credentials)
				assert.Empty(t, connections)
				return
			}
			assert.Equal(t, []corev1alpha1.MonitoringCredential{{
				Profile: corev1alpha1.CredentialProfileMonitor, SecretRef: commonv1alpha1.SecretRef{Name: "db-monitoring-monitor"},
			}}, credentials)
			assert.Equal(t, []corev1alpha1.ConnectionEndpoint{{
				Component:   common.ComponentEngine,
				PodSelector: map[string]string{controller.InstanceLabel: monTestInstance, controller.ComponentLabel: common.ComponentEngine},
				Container:   "pxc",
				Port:        intstr.FromString("mysql"),
				TLS:         tt.wantTLS,
				Auth:        &corev1alpha1.MetricsEndpointAuth{CredentialProfile: corev1alpha1.CredentialProfileMonitor},
			}}, connections)
		})
	}
}
