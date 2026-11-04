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
	"bytes"
	"errors"
	"fmt"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/go-ini/ini"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-xtradb-cluster/definition"
)

const (
	certManagerGroup = "cert-manager.io"

	// caKey is the key holding the CA certificate, in the certificate
	// secrets and in the connection secret.
	caKey = "ca.crt"

	// requireSecureTransportKey is the my.cnf variable that rejects client
	// connections without TLS.
	requireSecureTransportKey = "require_secure_transport"

	// tlsToggleToken approves turning TLS on or off on an existing cluster:
	// the operator briefly pauses the cluster to do it.
	tlsToggleToken = "pxc-tls-toggle"
)

// certificateSecretKeys are the keys a certificate secret must hold.
var certificateSecretKeys = []string{"tls.crt", "tls.key", caKey}

// tlsParameters returns the cluster's TLS parameters with defaults applied.
func tlsParameters(c *controller.Context) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := c.Instance().Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := c.DecodeParameters(&params); err != nil {
			return definition.TLSParameters{}, fmt.Errorf("decode parameters: %w", err)
		}
	}

	tls := definition.TLSParameters{}
	if params.TLS != nil {
		tls = *params.TLS
	}
	if tls.Mode == "" {
		tls.Mode = definition.TLSModePrefer
	}
	if tls.Issuer != nil && tls.Issuer.Name == "" {
		tls.Issuer = nil
	}
	if tls.Issuer != nil && tls.Issuer.Kind == "" {
		tls.Issuer.Kind = definition.IssuerKindIssuer
	}

	return tls, nil
}

func tlsEnabled(mode definition.TLSMode) bool {
	return mode != definition.TLSModeDisabled
}

// clientCertificateSecretName is the secret holding the certificate clients
// connect to; the operator generates it under its default name unless the
// cluster brings its own.
func clientCertificateSecretName(c *controller.Context, tls definition.TLSParameters) string {
	if tls.CertificateSecretName != "" {
		return tls.CertificateSecretName
	}
	return c.Name() + "-ssl"
}

// internalCertificateSecretName is the secret holding the certificate
// members authenticate each other with.
func internalCertificateSecretName(c *controller.Context, tls definition.TLSParameters) string {
	if tls.InternalCertificateSecretName != "" {
		return tls.InternalCertificateSecretName
	}
	return c.Name() + "-ssl-internal"
}

// configureTLS sets the TLS mode and the certificate source on the PXC
// resource; the certificate secret names are set with the other secrets.
func configureTLS(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, tls definition.TLSParameters) {
	enabled := tlsEnabled(tls.Mode)
	spec := &pxcv1.TLSSpec{Enabled: &enabled}

	if tls.Issuer != nil {
		spec.IssuerConf = &cmmeta.IssuerReference{
			Name:  tls.Issuer.Name,
			Kind:  string(tls.Issuer.Kind),
			Group: certManagerGroup,
		}
	}

	pxc.Spec.TLS = spec
	pxc.Spec.Unsafe.TLS = !enabled
	pxc.Spec.SSLSecretName = tls.CertificateSecretName
	pxc.Spec.SSLInternalSecretName = internalCertificateSecretName(c, tls)
}

// gateTLSToggle holds back a change to whether TLS is enabled on an existing
// cluster until approved: the operator briefly pauses the cluster to apply
// it. New clusters need no gate. On hold, pxc is left with the cluster's
// current TLS state instead of the requested one.
func gateTLSToggle(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) error {
	current := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PXC CR: %w", err)
	}

	currentEnabled := current.Spec.TLS == nil || current.Spec.TLS.Enabled == nil || *current.Spec.TLS.Enabled
	wantEnabled := pxc.Spec.TLS != nil && pxc.Spec.TLS.Enabled != nil && *pxc.Spec.TLS.Enabled
	if currentEnabled == wantEnabled {
		return nil
	}

	if !c.RequestMaintenance(tlsToggleToken,
		"Turn TLS on or off (briefly pauses the cluster)",
		controller.MaintenanceDowntime) {
		pxc.Spec.TLS.Enabled = &currentEnabled
		pxc.Spec.Unsafe.TLS = !currentEnabled
	}

	return nil
}

// applyRequireSecureTransport sets require_secure_transport=ON in the
// [mysqld] section of the engine's my.cnf configuration when TLS is
// required, keeping a value the user set explicitly. A configuration that
// isn't valid ini is passed through for the operator to reject.
func applyRequireSecureTransport(configuration string) (string, error) {
	cfg, err := ini.Load([]byte(configuration))
	if err != nil {
		return configuration, nil //nolint:nilerr // see above
	}

	section := cfg.Section("mysqld")
	if section.HasKey(requireSecureTransportKey) {
		return configuration, nil
	}
	section.NewKey(requireSecureTransportKey, "ON") //nolint:errcheck // NewKey only fails on an empty key name

	var out bytes.Buffer
	if _, err := cfg.WriteTo(&out); err != nil {
		return "", fmt.Errorf("render configuration: %w", err)
	}
	return out.String(), nil
}

// validateTLS validates the TLS parameters, and that they keep what can't
// change on an existing cluster.
func validateTLS(c *controller.Context) error {
	tls, err := tlsParameters(c)
	if err != nil {
		return err
	}

	switch tls.Mode {
	case definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled:
	default:
		return fmt.Errorf("mode must be one of %s, %s or %s, got %q",
			definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled, tls.Mode)
	}

	if tls.Issuer != nil {
		switch tls.Issuer.Kind {
		case definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer:
		default:
			return fmt.Errorf("issuer.kind must be %s or %s, got %q",
				definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer, tls.Issuer.Kind)
		}
	}

	customCertificates := tls.CertificateSecretName != "" || tls.InternalCertificateSecretName != ""
	switch {
	case !tlsEnabled(tls.Mode) && (customCertificates || tls.Issuer != nil):
		return errors.New("certificates and issuer apply only with TLS enabled")
	case tls.InternalCertificateSecretName != "" && tls.CertificateSecretName == "":
		return errors.New("internalCertificateSecretName requires certificateSecretName")
	case tls.CertificateSecretName != "" && tls.Issuer != nil:
		return errors.New("use either certificate secrets or an issuer, not both")
	}

	for _, name := range []string{tls.CertificateSecretName, tls.InternalCertificateSecretName} {
		if name == "" {
			continue
		}
		if err := validateCertificateSecret(c, name); err != nil {
			return err
		}
	}

	return validateTLSChange(c, tls)
}

func validateCertificateSecret(c *controller.Context, name string) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		return fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			return fmt.Errorf("certificate secret %q is missing %q", name, key)
		}
	}
	return nil
}

// validateTLSChange rejects a certificate source change the operator can't
// apply to an existing cluster: a new certificate source brings a new CA,
// which members still on the old one wouldn't trust. Turning TLS on or off
// is left to gateTLSToggle, which the operator can apply safely.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters) error {
	current := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PXC CR: %w", err)
	}

	currentIssuer := "/"
	if current.Spec.TLS != nil && current.Spec.TLS.IssuerConf != nil {
		currentIssuer = current.Spec.TLS.IssuerConf.Kind + "/" + current.Spec.TLS.IssuerConf.Name
	}
	wantIssuer := "/"
	if tls.Issuer != nil {
		wantIssuer = string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
	}
	if currentIssuer != wantIssuer || current.Spec.SSLSecretName != tls.CertificateSecretName {
		return errors.New("certificates and issuer can't change on an existing cluster")
	}

	return nil
}

// tlsConnection returns the CA clients verify the cluster with, and whether
// clients must use TLS.
func tlsConnection(c *controller.Context) (ca []byte, required bool, err error) {
	tls, err := tlsParameters(c)
	if err != nil || !tlsEnabled(tls.Mode) {
		return nil, false, err
	}

	secret := &corev1.Secret{}
	if err := c.Get(secret, clientCertificateSecretName(c, tls)); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, tls.Mode == definition.TLSModeRequire, nil
		}
		return nil, false, fmt.Errorf("get certificate secret: %w", err)
	}

	return secret.Data[caKey], tls.Mode == definition.TLSModeRequire, nil
}
