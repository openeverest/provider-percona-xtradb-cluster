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

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/definition"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
	tls, err := tlsParameters(c.Instance())
	require.NoError(t, err)
	assert.Equal(t, definition.TLSParameters{Mode: definition.TLSModePrefer}, tls)
}

func TestTLSParametersDefaultsIssuerKind(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, `{"tls":{"mode":"requireTLS","issuer":{"name":"my-issuer"}}}`)
	tls, err := tlsParameters(c.Instance())
	require.NoError(t, err)
	require.NotNil(t, tls.Issuer)
	assert.Equal(t, definition.IssuerKindIssuer, tls.Issuer.Kind)
}

func TestTLSParametersDropsNamelessIssuer(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, `{"tls":{"issuer":{"kind":"ClusterIssuer"}}}`)
	tls, err := tlsParameters(c.Instance())
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

func TestTLSParametersDefaultsIssuerGroup(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, `{"tls":{"issuer":{"name":"corp-ca"}}}`)
	tls, err := tlsParameters(c.Instance())
	require.NoError(t, err)
	assert.Equal(t, certManagerGroup, tls.Issuer.Group)
}

func TestConfigureTLSExternalIssuerAndNames(t *testing.T) {
	t.Parallel()

	c := tlsContext(t, "")
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, definition.TLSParameters{
		Mode:            definition.TLSModePrefer,
		Issuer:          &definition.IssuerReference{Name: "pca", Kind: "AWSPCAClusterIssuer", Group: "awspca.cert-manager.io"},
		AdditionalNames: []string{"db.example.com"},
	})

	assert.Equal(t, "awspca.cert-manager.io", pxc.Spec.TLS.IssuerConf.Group)
	assert.Equal(t, "AWSPCAClusterIssuer", pxc.Spec.TLS.IssuerConf.Kind)
	assert.Equal(t, []string{"db.example.com"}, pxc.Spec.TLS.SANs)
}

// existingPXC is the cluster's current PerconaXtraDBCluster with TLS enabled
// and the given additional names.
func existingPXC(names ...string) *pxcv1.PerconaXtraDBCluster {
	enabled := true
	return &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterSpec{TLS: &pxcv1.TLSSpec{Enabled: &enabled, SANs: names}},
	}
}

// operatorCertificate is a cert-manager Certificate the operator created.
func operatorCertificate(name string, dnsNames ...string) *unstructured.Unstructured {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(name)
	cert.SetNamespace(tlsTestNamespace)
	names := make([]any, 0, len(dnsNames))
	for _, n := range dnsNames {
		names = append(names, n)
	}
	cert.Object["spec"] = map[string]any{"dnsNames": names}
	return cert
}

func certificateNames(t *testing.T, c *controller.Context, name string) []string {
	t.Helper()
	cert, err := getCertificate(c, name)
	require.NoError(t, err)
	require.NotNil(t, cert)
	names, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	require.NoError(t, err)
	return names
}

func TestGateTLSNamesNewCluster(t *testing.T) {
	t.Parallel()

	tls := definition.TLSParameters{Mode: definition.TLSModePrefer, AdditionalNames: []string{"db.example.com"}}
	c := tlsContext(t, "")
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, tls)

	require.NoError(t, gateTLSNames(c, pxc, tls))
	assert.Equal(t, []string{"db.example.com"}, pxc.Spec.TLS.SANs)
	assert.Empty(t, c.GetPendingMaintenance())
}

func TestGateTLSNamesHoldsUnapprovedChange(t *testing.T) {
	t.Parallel()

	tls := definition.TLSParameters{Mode: definition.TLSModePrefer, AdditionalNames: []string{"new.example.com"}}
	c := tlsContext(t, "", existingPXC("old.example.com"),
		operatorCertificate(tlsTestInstance+"-ssl", "db-pxc", "old.example.com"))
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, tls)

	require.NoError(t, gateTLSNames(c, pxc, tls))
	assert.Equal(t, []string{"old.example.com"}, pxc.Spec.TLS.SANs)
	require.Len(t, c.GetPendingMaintenance(), 1)
	assert.Equal(t, tlsNamesToken(tls.AdditionalNames), c.GetPendingMaintenance()[0].ApprovalToken)
	assert.Equal(t, controller.MaintenanceRollingRestart, c.GetPendingMaintenance()[0].Severity)
	assert.Equal(t, []string{"db-pxc", "old.example.com"}, certificateNames(t, c, tlsTestInstance+"-ssl"))
}

func TestGateTLSNamesReissuesApprovedChange(t *testing.T) {
	t.Parallel()

	wanted := []string{"new.example.com"}
	tls := definition.TLSParameters{Mode: definition.TLSModePrefer, AdditionalNames: wanted}
	c := tlsContext(t, `{"tls":{"additionalNames":["new.example.com"]}}`, existingPXC("old.example.com"),
		operatorCertificate(tlsTestInstance+"-ssl", "db-pxc", "*.db-pxc", "old.example.com"),
		operatorCertificate(tlsTestInstance+"-ssl-internal", "db-pxc", "old.example.com", "db-haproxy"))
	c.Instance().Spec.Maintenance = &corev1alpha1.MaintenanceSpec{Approved: tlsNamesToken(wanted)}
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, tls)

	require.NoError(t, gateTLSNames(c, pxc, tls))
	assert.Equal(t, wanted, pxc.Spec.TLS.SANs)
	assert.Empty(t, c.GetPendingMaintenance())
	assert.Equal(t, []string{"db-pxc", "*.db-pxc", "new.example.com"}, certificateNames(t, c, tlsTestInstance+"-ssl"))
	assert.Equal(t, []string{"db-pxc", "db-haproxy", "new.example.com"}, certificateNames(t, c, tlsTestInstance+"-ssl-internal"))
}

func TestGateTLSNamesKeepsNamesWhileTurningTLSOff(t *testing.T) {
	t.Parallel()

	tls := definition.TLSParameters{Mode: definition.TLSModeDisabled}
	c := tlsContext(t, "", existingPXC("old.example.com"))
	pxc := &pxcv1.PerconaXtraDBCluster{}
	configureTLS(c, pxc, tls)
	require.NoError(t, gateTLSToggle(c, pxc))

	require.NoError(t, gateTLSNames(c, pxc, tls))
	assert.Equal(t, []string{"old.example.com"}, pxc.Spec.TLS.SANs)
	require.Len(t, c.GetPendingMaintenance(), 1)
	assert.Equal(t, tlsToggleToken, c.GetPendingMaintenance()[0].ApprovalToken)
}

func TestValidateReferencesAdditionalNamesChange(t *testing.T) {
	t.Parallel()

	params := `{"tls":{"additionalNames":["db.example.com"]}}`

	// Certificates issued by cert-manager can be reissued with the names.
	c := tlsContext(t, params, existingPXC(), operatorCertificate(tlsTestInstance+"-ssl", "db-pxc"))
	errs, err := (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	assert.Empty(t, errs)

	// Certificates from the CA the operator generated can't.
	c = tlsContext(t, params, existingPXC())
	errs, err = (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	assert.Equal(t, "spec.parameters.tls.additionalNames", errs[0].Field)

	// Unchanged names are fine either way.
	c = tlsContext(t, params, existingPXC("db.example.com"))
	errs, err = (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	assert.Empty(t, errs)
}

func TestValidateReferencesRejectsIssuerGroupChange(t *testing.T) {
	t.Parallel()

	existing := existingPXC()
	existing.Spec.TLS.IssuerConf = &cmmeta.IssuerReference{Name: "corp-ca", Kind: "Issuer", Group: certManagerGroup}
	c := tlsContext(t, `{"tls":{"issuer":{"name":"corp-ca","group":"awspca.cert-manager.io"}}}`, existing)

	errs, err := (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "certificates and issuer can't change")

	// The default group matches clusters created before it could be set.
	c = tlsContext(t, `{"tls":{"issuer":{"name":"corp-ca"}}}`, existing)
	errs, err = (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	assert.Empty(t, errs)
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

func TestValidateTLSParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		tls       definition.TLSParameters
		wantField string
		wantErr   string
	}{
		{
			name: "defaults ok",
			tls:  definition.TLSParameters{Mode: definition.TLSModePrefer},
		},
		{
			name:      "invalid mode",
			tls:       definition.TLSParameters{Mode: "bogus"},
			wantField: "spec.parameters.tls.mode",
			wantErr:   "supported values",
		},
		{
			name: "invalid issuer kind",
			tls: definition.TLSParameters{
				Mode:   definition.TLSModePrefer,
				Issuer: &definition.IssuerReference{Name: "my-issuer", Kind: "bogus"},
			},
			wantField: "spec.parameters.tls.issuer.kind",
			wantErr:   "supported values",
		},
		{
			name: "disabled with certificate",
			tls: definition.TLSParameters{
				Mode:                  definition.TLSModeDisabled,
				CertificateSecretName: "my-tls",
			},
			wantField: "spec.parameters.tls",
			wantErr:   "apply only with TLS enabled",
		},
		{
			name: "disabled with additional names",
			tls: definition.TLSParameters{
				Mode:            definition.TLSModeDisabled,
				AdditionalNames: []string{"db.example.com"},
			},
			wantField: "spec.parameters.tls",
			wantErr:   "apply only with TLS enabled",
		},
		{
			name: "external issuer of another group ok",
			tls: definition.TLSParameters{
				Mode:   definition.TLSModePrefer,
				Issuer: &definition.IssuerReference{Name: "pca", Kind: "AWSPCAClusterIssuer", Group: "awspca.cert-manager.io"},
			},
		},
		{
			name: "invalid issuer group",
			tls: definition.TLSParameters{
				Mode:   definition.TLSModePrefer,
				Issuer: &definition.IssuerReference{Name: "pca", Kind: "Issuer", Group: "Not_A_Group"},
			},
			wantField: "spec.parameters.tls.issuer.group",
		},
		{
			name: "additional names ok",
			tls: definition.TLSParameters{
				Mode:            definition.TLSModePrefer,
				Issuer:          &definition.IssuerReference{Name: "corp-ca", Kind: definition.IssuerKindIssuer, Group: certManagerGroup},
				AdditionalNames: []string{"db.example.com", "*.db.example.com"},
			},
		},
		{
			name: "additional name is an IP",
			tls: definition.TLSParameters{
				Mode:            definition.TLSModePrefer,
				AdditionalNames: []string{"10.0.0.1"},
			},
			wantField: "spec.parameters.tls.additionalNames[0]",
			wantErr:   "not an IP address",
		},
		{
			name: "additional name is not a DNS name",
			tls: definition.TLSParameters{
				Mode:            definition.TLSModePrefer,
				AdditionalNames: []string{"DB_Example"},
			},
			wantField: "spec.parameters.tls.additionalNames[0]",
			wantErr:   "must be a DNS name",
		},
		{
			name: "duplicate additional names",
			tls: definition.TLSParameters{
				Mode:            definition.TLSModePrefer,
				AdditionalNames: []string{"db.example.com", "db.example.com"},
			},
			wantField: "spec.parameters.tls.additionalNames[1]",
			wantErr:   "Duplicate",
		},
		{
			name: "additional names with certificate secret",
			tls: definition.TLSParameters{
				Mode:                  definition.TLSModePrefer,
				CertificateSecretName: "my-tls",
				AdditionalNames:       []string{"db.example.com"},
			},
			wantField: "spec.parameters.tls.additionalNames",
			wantErr:   "carries its own names",
		},
		{
			name: "internal without external",
			tls: definition.TLSParameters{
				Mode:                          definition.TLSModePrefer,
				InternalCertificateSecretName: "my-tls-internal",
			},
			wantField: "spec.parameters.tls.certificateSecretName",
		},
		{
			name: "certificate and issuer",
			tls: definition.TLSParameters{
				Mode:                  definition.TLSModePrefer,
				CertificateSecretName: "my-tls",
				Issuer:                &definition.IssuerReference{Name: "my-issuer", Kind: definition.IssuerKindIssuer},
			},
			wantField: "spec.parameters.tls",
			wantErr:   "use either certificate secrets or an issuer, not both",
		},
		{
			name: "custom certificate ok",
			tls: definition.TLSParameters{
				Mode:                  definition.TLSModePrefer,
				CertificateSecretName: "my-tls",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := validateTLSParameters(tt.tls, field.NewPath("spec", "parameters", "tls"))
			if tt.wantField == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Equal(t, tt.wantField, errs[0].Field)
			if tt.wantErr != "" {
				assert.Contains(t, errs[0].Error(), tt.wantErr)
			}
		})
	}
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
