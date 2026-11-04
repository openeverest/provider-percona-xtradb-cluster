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
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/definition"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	tlsTestInstance  = "db"
	tlsTestNamespace = "team-a"
)

// tlsContext returns the context of an Instance with the given raw
// spec.parameters; objs are what the cluster holds besides the Instance.
func tlsContext(t *testing.T, parameters string, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace, UID: "instance-uid"},
	}
	if parameters != "" {
		in.Spec.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in)...).Build()
	return controller.NewContext(context.Background(), cl, in, "percona-xtradb-cluster")
}

func certSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tlsTestNamespace},
		Data: map[string][]byte{
			"tls.crt": []byte("cert"),
			"tls.key": []byte("key"),
			"ca.crt":  []byte("ca"),
		},
	}
}

func TestTLSParametersDefaults(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, "")
	tls, err := tlsParameters(c)
	require.NoError(t, err)
	assert.Equal(t, definition.TLSParameters{Mode: definition.TLSModePrefer}, tls)
}

func TestTLSParametersDefaultsIssuerKind(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, `{"tls":{"mode":"requireTLS","issuer":{"name":"my-issuer"}}}`)
	tls, err := tlsParameters(c)
	require.NoError(t, err)
	require.NotNil(t, tls.Issuer)
	assert.Equal(t, definition.IssuerKindIssuer, tls.Issuer.Kind)
}

func TestTLSParametersDropsNamelessIssuer(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, `{"tls":{"issuer":{"kind":"ClusterIssuer"}}}`)
	tls, err := tlsParameters(c)
	require.NoError(t, err)
	assert.Nil(t, tls.Issuer)
}

func TestConfigureTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tls        definition.TLSParameters
		wantSSL    string
		wantEnable bool
	}{
		{
			name:       "prefer",
			tls:        definition.TLSParameters{Mode: definition.TLSModePrefer},
			wantEnable: true,
		},
		{
			name:       "disabled",
			tls:        definition.TLSParameters{Mode: definition.TLSModeDisabled},
			wantEnable: false,
		},
		{
			name:       "custom certificate",
			tls:        definition.TLSParameters{Mode: definition.TLSModePrefer, CertificateSecretName: "my-tls"},
			wantSSL:    "my-tls",
			wantEnable: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tlsContext(t, "")
			pxc := &pxcv1.PerconaXtraDBCluster{}
			configureTLS(c, pxc, tt.tls)

			require.NotNil(t, pxc.Spec.TLS)
			require.NotNil(t, pxc.Spec.TLS.Enabled)
			assert.Equal(t, tt.wantEnable, *pxc.Spec.TLS.Enabled)
			assert.Equal(t, !tt.wantEnable, pxc.Spec.Unsafe.TLS)
			assert.Equal(t, tt.wantSSL, pxc.Spec.SSLSecretName)
			assert.Equal(t, tlsTestInstance+"-ssl-internal", pxc.Spec.SSLInternalSecretName)
		})
	}
}

func TestConfigureTLSIssuer(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, "")
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, definition.TLSParameters{
		Mode:   definition.TLSModePrefer,
		Issuer: &definition.IssuerReference{Name: "corp-ca", Kind: definition.IssuerKindClusterIssuer},
	})

	require.NotNil(t, pxc.Spec.TLS.IssuerConf)
	assert.Equal(t, "corp-ca", pxc.Spec.TLS.IssuerConf.Name)
	assert.Equal(t, "ClusterIssuer", pxc.Spec.TLS.IssuerConf.Kind)
	assert.Equal(t, certManagerGroup, pxc.Spec.TLS.IssuerConf.Group)
}

func TestGateTLSToggleNewCluster(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, "")
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, definition.TLSParameters{Mode: definition.TLSModeDisabled})

	require.NoError(t, gateTLSToggle(c, pxc))
	// No existing cluster to compare against: nothing is held back.
	assert.False(t, *pxc.Spec.TLS.Enabled)
}

func TestGateTLSToggleHoldsUnapprovedChange(t *testing.T) {
	t.Parallel()

	enabled := true
	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterSpec{TLS: &pxcv1.TLSSpec{Enabled: &enabled}},
	}
	c := tlsContext(t, `{"tls":{"mode":"disabled"}}`, existing)

	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, definition.TLSParameters{Mode: definition.TLSModeDisabled})
	require.NoError(t, gateTLSToggle(c, pxc))

	// Held: the cluster keeps TLS enabled until the change is approved.
	assert.True(t, *pxc.Spec.TLS.Enabled)
	assert.False(t, pxc.Spec.Unsafe.TLS)
	require.Len(t, c.GetPendingMaintenance(), 1)
	assert.Equal(t, tlsToggleToken, c.GetPendingMaintenance()[0].ApprovalToken)
}

func TestGateTLSToggleNoChange(t *testing.T) {
	t.Parallel()

	enabled := true
	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterSpec{TLS: &pxcv1.TLSSpec{Enabled: &enabled}},
	}
	c := tlsContext(t, "", existing)

	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, definition.TLSParameters{Mode: definition.TLSModePrefer})
	require.NoError(t, gateTLSToggle(c, pxc))

	assert.True(t, *pxc.Spec.TLS.Enabled)
	assert.Empty(t, c.GetPendingMaintenance())
}

func TestApplyRequireSecureTransport(t *testing.T) {
	t.Parallel()

	out, err := applyRequireSecureTransport("[mysqld]\nmax_connections = 100\n")
	require.NoError(t, err)
	assert.Contains(t, out, "require_secure_transport = ON")
	assert.Contains(t, out, "max_connections")
}

func TestApplyRequireSecureTransportKeepsUserValue(t *testing.T) {
	t.Parallel()

	out, err := applyRequireSecureTransport("[mysqld]\nrequire_secure_transport = OFF\n")
	require.NoError(t, err)
	assert.Equal(t, "[mysqld]\nrequire_secure_transport = OFF\n", out)
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		parameters string
		objs       []client.Object
		wantErr    string
	}{
		{
			name:       "defaults ok",
			parameters: "",
		},
		{
			name:       "invalid mode",
			parameters: `{"tls":{"mode":"bogus"}}`,
			wantErr:    "mode must be one of",
		},
		{
			name:       "disabled with certificate",
			parameters: `{"tls":{"mode":"disabled","certificateSecretName":"my-tls"}}`,
			objs:       []client.Object{certSecret("my-tls")},
			wantErr:    "certificates and issuer apply only with TLS enabled",
		},
		{
			name:       "internal without external",
			parameters: `{"tls":{"internalCertificateSecretName":"my-tls-internal"}}`,
			objs:       []client.Object{certSecret("my-tls-internal")},
			wantErr:    "internalCertificateSecretName requires certificateSecretName",
		},
		{
			name:       "certificate and issuer",
			parameters: `{"tls":{"certificateSecretName":"my-tls","issuer":{"name":"my-issuer"}}}`,
			objs:       []client.Object{certSecret("my-tls")},
			wantErr:    "use either certificate secrets or an issuer, not both",
		},
		{
			name:       "missing certificate secret",
			parameters: `{"tls":{"certificateSecretName":"missing"}}`,
			wantErr:    `get certificate secret "missing"`,
		},
		{
			name:       "custom certificate ok",
			parameters: `{"tls":{"certificateSecretName":"my-tls"}}`,
			objs:       []client.Object{certSecret("my-tls")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tlsContext(t, tt.parameters, tt.objs...)
			err := validateTLS(c)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestValidateTLSChangeRejectsCertificateSourceChange(t *testing.T) {
	t.Parallel()

	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterSpec{SSLSecretName: "old-tls"},
	}
	c := tlsContext(t, `{"tls":{"certificateSecretName":"new-tls"}}`, existing, certSecret("new-tls"))

	err := validateTLS(c)
	require.Error(t, err)
	assert.ErrorContains(t, err, "certificates and issuer can't change")
}

func TestValidateTLSChangeAllowsModeChange(t *testing.T) {
	t.Parallel()

	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
	}
	c := tlsContext(t, `{"tls":{"mode":"requireTLS"}}`, existing)

	require.NoError(t, validateTLS(c))
}

func TestTLSConnection(t *testing.T) {
	t.Parallel()

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		c := tlsContext(t, `{"tls":{"mode":"disabled"}}`)
		ca, required, err := tlsConnection(c)
		require.NoError(t, err)
		assert.Nil(t, ca)
		assert.False(t, required)
	})

	t.Run("prefer, secret not yet created", func(t *testing.T) {
		t.Parallel()
		c := tlsContext(t, "")
		ca, required, err := tlsConnection(c)
		require.NoError(t, err)
		assert.Nil(t, ca)
		assert.False(t, required)
	})

	t.Run("require, secret exists", func(t *testing.T) {
		t.Parallel()
		c := tlsContext(t, `{"tls":{"mode":"requireTLS"}}`, certSecret(tlsTestInstance+"-ssl"))
		ca, required, err := tlsConnection(c)
		require.NoError(t, err)
		assert.Equal(t, []byte("ca"), ca)
		assert.True(t, required)
	})
}
