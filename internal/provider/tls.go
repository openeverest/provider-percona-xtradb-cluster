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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"

	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/go-ini/ini"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-xtradb-cluster/definition"
)

var _ controller.ReferenceValidator = (*PXCProvider)(nil)

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

	// tlsNamesTokenPrefix starts the token approving a change to the
	// additional names of a running cluster's certificates, followed by a
	// hash of the names it leads to.
	tlsNamesTokenPrefix = "tls-names-"
)

// certificateGVK is the cert-manager Certificate the operator creates for
// each certificate secret when it issues them through cert-manager.
var certificateGVK = schema.GroupVersionKind{Group: certManagerGroup, Version: "v1", Kind: "Certificate"}

// certificateSecretKeys are the keys a certificate secret must hold.
var certificateSecretKeys = []string{"tls.crt", "tls.key", caKey}

// tlsParameters returns the cluster's TLS parameters with defaults applied.
func tlsParameters(instance *corev1alpha1.Instance) (definition.TLSParameters, error) {
	params := definition.Parameters{}
	if raw := instance.Spec.Parameters; raw != nil && len(raw.Raw) > 0 {
		if err := json.Unmarshal(raw.Raw, &params); err != nil {
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
	if tls.Issuer != nil && tls.Issuer.Group == "" {
		tls.Issuer.Group = certManagerGroup
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
			Group: tls.Issuer.Group,
		}
	}
	if enabled {
		spec.SANs = slices.Clone(tls.AdditionalNames)
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

	currentEnabled := tlsSpecEnabled(current.Spec.TLS)
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

// tlsSpecEnabled reports whether a PXC resource's TLS spec enables TLS, which
// the operator does unless told otherwise.
func tlsSpecEnabled(spec *pxcv1.TLSSpec) bool {
	return spec == nil || spec.Enabled == nil || *spec.Enabled
}

func appliedTLSNames(pxc *pxcv1.PerconaXtraDBCluster) []string {
	if pxc.Spec.TLS == nil {
		return nil
	}
	return pxc.Spec.TLS.SANs
}

// gateTLSNames holds back a change to the additional names of a running
// cluster with TLS until approved: the operator never updates certificates it
// has issued, so the provider has cert-manager reissue them, and the operator
// restarts the pods to load them. A cluster that is created or gets TLS now
// has its certificates issued with the names, without approval. On hold, or
// while TLS is being turned off, pxc keeps the names currently applied.
func gateTLSNames(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, tls definition.TLSParameters) error {
	current := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("fetching current PXC CR: %w", err)
	}
	if !tlsSpecEnabled(current.Spec.TLS) || !tlsSpecEnabled(pxc.Spec.TLS) {
		return nil
	}

	applied := appliedTLSNames(current)
	if !tlsEnabled(tls.Mode) {
		pxc.Spec.TLS.SANs = slices.Clone(applied)
		return nil
	}
	wanted := pxc.Spec.TLS.SANs
	if slices.Equal(applied, wanted) {
		return nil
	}

	description := "Reissue the certificates without additional names (rolling restart)"
	if len(wanted) > 0 {
		description = fmt.Sprintf("Reissue the certificates for the additional names %s (rolling restart)",
			strings.Join(wanted, ", "))
	}
	if !c.RequestMaintenance(tlsNamesToken(wanted), description, controller.MaintenanceRollingRestart) {
		pxc.Spec.TLS.SANs = slices.Clone(applied)
		return nil
	}

	return reissueCertificates(c, applied, wanted)
}

// tlsNamesToken identifies a change to the additional names by the names it
// leads to.
func tlsNamesToken(names []string) string {
	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	return tlsNamesTokenPrefix + hex.EncodeToString(sum[:])[:10]
}

// operatorCertificateNames are the cert-manager Certificates the operator
// creates for a cluster's certificate secrets.
func operatorCertificateNames(name string) []string {
	return []string{name + "-ssl", name + "-ssl-internal"}
}

// reissueCertificates replaces the applied additional names with the wanted
// ones on the cert-manager Certificates the operator created for the
// cluster. cert-manager then reissues each certificate from the same issuer,
// so the CA members trust is unchanged.
func reissueCertificates(c *controller.Context, applied, wanted []string) error {
	for _, name := range operatorCertificateNames(c.Name()) {
		cert, err := getCertificate(c, name)
		if err != nil {
			return err
		}
		if cert == nil {
			continue
		}

		dnsNames, _, err := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
		if err != nil {
			return fmt.Errorf("read the names of Certificate %s: %w", name, err)
		}
		names := replaceNames(dnsNames, applied, wanted)
		if slices.Equal(names, dnsNames) {
			continue
		}

		patch, err := json.Marshal(map[string]any{"spec": map[string]any{"dnsNames": names}})
		if err != nil {
			return fmt.Errorf("encode the names of Certificate %s: %w", name, err)
		}
		log.FromContext(c.Context()).Info("Reissuing the certificate for its additional names",
			"certificate", name, "additionalNames", wanted)
		if err := c.Client().Patch(c.Context(), cert, client.RawPatch(types.MergePatchType, patch)); err != nil {
			return fmt.Errorf("update the names of Certificate %s: %w", name, err)
		}
	}
	return nil
}

// replaceNames drops the applied names from names and appends the wanted
// ones it lacks, keeping the order of the rest.
func replaceNames(names, applied, wanted []string) []string {
	out := slices.DeleteFunc(slices.Clone(names), func(n string) bool { return slices.Contains(applied, n) })
	for _, n := range wanted {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// getCertificate returns the named cert-manager Certificate, or nil when it
// doesn't exist or cert-manager isn't installed.
func getCertificate(c *controller.Context, name string) (*unstructured.Unstructured, error) {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	if err := c.Get(cert, name); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get Certificate %s: %w", name, err)
	}
	return cert, nil
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

func validateTLSParameters(tls definition.TLSParameters, path *field.Path) field.ErrorList {
	var errs field.ErrorList

	switch tls.Mode {
	case definition.TLSModePrefer, definition.TLSModeRequire, definition.TLSModeDisabled:
	default:
		errs = append(errs, field.NotSupported(path.Child("mode"), tls.Mode,
			[]string{string(definition.TLSModePrefer), string(definition.TLSModeRequire), string(definition.TLSModeDisabled)},
		))
	}

	if tls.Issuer != nil {
		errs = append(errs, validateIssuer(*tls.Issuer, path.Child("issuer"))...)
	}
	errs = append(errs, validateAdditionalNames(tls, path.Child("additionalNames"))...)

	customCertificates := tls.CertificateSecretName != "" || tls.InternalCertificateSecretName != ""
	switch {
	case !tlsEnabled(tls.Mode) && (customCertificates || tls.Issuer != nil || len(tls.AdditionalNames) > 0):
		errs = append(errs, field.Invalid(path, field.OmitValueType{},
			"certificates, issuer and additionalNames apply only with TLS enabled"))
	case tls.InternalCertificateSecretName != "" && tls.CertificateSecretName == "":
		errs = append(errs, field.Required(path.Child("certificateSecretName"),
			"required when internalCertificateSecretName is set"))
	case tls.CertificateSecretName != "" && tls.Issuer != nil:
		errs = append(errs, field.Invalid(path, field.OmitValueType{},
			"use either certificate secrets or an issuer, not both"))
	}

	return errs
}

func validateIssuer(issuer definition.IssuerReference, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if msgs := validation.IsDNS1123Subdomain(issuer.Group); len(msgs) > 0 {
		errs = append(errs, field.Invalid(path.Child("group"), issuer.Group, strings.Join(msgs, "; ")))
	}
	if issuer.Group != certManagerGroup {
		return errs
	}
	switch issuer.Kind {
	case definition.IssuerKindIssuer, definition.IssuerKindClusterIssuer:
	default:
		errs = append(errs, field.NotSupported(path.Child("kind"), issuer.Kind,
			[]string{string(definition.IssuerKindIssuer), string(definition.IssuerKindClusterIssuer)},
		))
	}
	return errs
}

func validateAdditionalNames(tls definition.TLSParameters, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if len(tls.AdditionalNames) > 0 && tls.CertificateSecretName != "" {
		errs = append(errs, field.Forbidden(path,
			"cannot be combined with certificateSecretName: that certificate carries its own names"))
	}
	seen := make(map[string]bool, len(tls.AdditionalNames))
	for i, name := range tls.AdditionalNames {
		switch {
		case net.ParseIP(name) != nil:
			errs = append(errs, field.Invalid(path.Index(i), name, "must be a DNS name, not an IP address"))
		case seen[name]:
			errs = append(errs, field.Duplicate(path.Index(i), name))
		default:
			if msgs := validation.IsDNS1123Subdomain(strings.TrimPrefix(name, "*.")); len(msgs) > 0 {
				errs = append(errs, field.Invalid(path.Index(i), name, "must be a DNS name: "+strings.Join(msgs, "; ")))
			}
		}
		seen[name] = true
	}
	return errs
}

func (p *PXCProvider) ValidateReferences(c *controller.Context) (field.ErrorList, error) {
	tls, err := tlsParameters(c.Instance())
	if err != nil {
		return nil, err
	}
	path := field.NewPath("spec", "parameters", "tls")

	var errs field.ErrorList
	for _, name := range []string{tls.CertificateSecretName, tls.InternalCertificateSecretName} {
		if name == "" {
			continue
		}
		secretErrs, err := validateCertificateSecret(c, path, name)
		if err != nil {
			return nil, err
		}
		errs = append(errs, secretErrs...)
	}

	changeErrs, err := validateTLSChange(c, tls, path)
	if err != nil {
		return nil, err
	}
	errs = append(errs, changeErrs...)

	return errs, nil
}

func validateCertificateSecret(c *controller.Context, path *field.Path, name string) (field.ErrorList, error) {
	secret := &corev1.Secret{}
	if err := c.Get(secret, name); err != nil {
		if apierrors.IsNotFound(err) {
			return field.ErrorList{field.NotFound(path, name)}, nil
		}
		return nil, fmt.Errorf("get certificate secret %q: %w", name, err)
	}
	var errs field.ErrorList
	for _, key := range certificateSecretKeys {
		if len(secret.Data[key]) == 0 {
			errs = append(errs, field.Invalid(path, name, fmt.Sprintf("secret is missing %q", key)))
		}
	}
	return errs, nil
}

// validateTLSChange rejects a certificate source change the operator can't
// apply to an existing cluster: a new certificate source brings a new CA,
// which members still on the old one wouldn't trust. Turning TLS on or off
// is left to gateTLSToggle, which the operator can apply safely. The same
// goes for new additional names on certificates the operator generated
// without cert-manager: it would issue them from a new CA.
func validateTLSChange(c *controller.Context, tls definition.TLSParameters, path *field.Path) (field.ErrorList, error) {
	current := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(current, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fetching current PXC CR: %w", err)
	}

	currentIssuer := ""
	if ref := current.Spec.TLS; ref != nil && ref.IssuerConf != nil {
		currentIssuer = ref.IssuerConf.Group + "/" + ref.IssuerConf.Kind + "/" + ref.IssuerConf.Name
	}
	wantIssuer := ""
	if tls.Issuer != nil {
		wantIssuer = tls.Issuer.Group + "/" + string(tls.Issuer.Kind) + "/" + tls.Issuer.Name
	}
	if currentIssuer != wantIssuer || current.Spec.SSLSecretName != tls.CertificateSecretName {
		return field.ErrorList{field.Forbidden(path, "certificates and issuer can't change on an existing cluster")}, nil
	}

	if !tlsSpecEnabled(current.Spec.TLS) || !tlsEnabled(tls.Mode) ||
		slices.Equal(appliedTLSNames(current), tls.AdditionalNames) {
		return nil, nil
	}
	cert, err := getCertificate(c, operatorCertificateNames(c.Name())[0])
	if err != nil {
		return nil, err
	}
	if cert == nil {
		return field.ErrorList{field.Forbidden(path.Child("additionalNames"),
			"can't change on this cluster: its certificates come from a CA the operator generated without "+
				"cert-manager, and new ones would come from a new CA its members don't trust")}, nil
	}

	return nil, nil
}

// tlsConnection returns the CA clients verify the cluster with, and whether
// clients must use TLS.
func tlsConnection(c *controller.Context) (ca []byte, required bool, err error) {
	tls, err := tlsParameters(c.Instance())
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
