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

// Package definition contains shared types used across the provider definition.
//
// +k8s:openapi-gen=true
package definition

// TopologyType defines the type of deployment topology.
type TopologyType string

const (
	// TopologyTypeCluster represents a PXC cluster topology.
	TopologyTypeCluster TopologyType = "cluster"
)

// Parameters defines provider-level structured parameters that apply to the
// entire cluster (Instance.spec.parameters).
type Parameters struct {
	// TLS configures encryption in transit, between clients and the cluster
	// and between its members.
	// +optional
	TLS *TLSParameters `json:"tls,omitempty"`
}

// TLSMode controls whether connections require TLS.
// +enum
type TLSMode string

const (
	// TLSModePrefer encrypts traffic between members and accepts clients with
	// or without TLS. The default.
	TLSModePrefer TLSMode = "preferTLS"
	// TLSModeRequire encrypts all traffic and rejects clients without TLS,
	// by setting require_secure_transport=ON on the engine.
	TLSModeRequire TLSMode = "requireTLS"
	// TLSModeDisabled turns TLS off. It can only be chosen when the cluster
	// is created.
	TLSModeDisabled TLSMode = "disabled"
)

// TLSParameters configures TLS for the cluster. The certificates come from,
// in order of precedence: the certificate secrets, the cert-manager issuer,
// or else are generated (through cert-manager when it is installed, or a
// self-signed CA otherwise). Certificates and issuer can only be set when
// the cluster is created.
type TLSParameters struct {
	// Mode controls whether connections require TLS.
	// +default="preferTLS"
	// +optional
	Mode TLSMode `json:"mode,omitempty"`

	// CertificateSecretName names a Secret in the cluster namespace with the
	// certificate clients connect to: tls.crt, tls.key and ca.crt. Its
	// subject alternative names must cover the cluster's service names.
	// +optional
	CertificateSecretName string `json:"certificateSecretName,omitempty"`

	// InternalCertificateSecretName names a Secret in the cluster namespace
	// with the certificate members authenticate each other with, in the
	// same format. Defaults to the certificate secret.
	// +optional
	InternalCertificateSecretName string `json:"internalCertificateSecretName,omitempty"`

	// Issuer is the cert-manager issuer that signs the cluster's
	// certificates, instead of a self-signed CA created for the cluster.
	// +optional
	Issuer *IssuerReference `json:"issuer,omitempty"`
}

// IssuerKind is the kind of a cert-manager issuer.
// +enum
type IssuerKind string

const (
	// IssuerKindIssuer is an Issuer in the cluster namespace.
	IssuerKindIssuer IssuerKind = "Issuer"
	// IssuerKindClusterIssuer is a cluster-wide ClusterIssuer.
	IssuerKindClusterIssuer IssuerKind = "ClusterIssuer"
)

// IssuerReference refers to a cert-manager issuer.
type IssuerReference struct {
	// Name of the issuer.
	// +optional
	Name string `json:"name,omitempty"`

	// Kind of the issuer.
	// +default="Issuer"
	// +optional
	Kind IssuerKind `json:"kind,omitempty"`
}
