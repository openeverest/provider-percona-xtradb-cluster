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
	"encoding/json"
	"fmt"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// pmmIntegration is the integration declared in definition/monitoring.yaml
// and matched by the pmm MonitoringClass.
const pmmIntegration = "pmm"

// haproxyMetricsPort is where the operator's HAProxy serves /metrics.
const haproxyMetricsPort = 8404

// pmmDestinationParameters is the part of a pmm MonitoringDestination's
// parameters the provider reads.
type pmmDestinationParameters struct {
	URL string `json:"url"`
}

// pmmEntryParameters is the shape of a pmm entry's parameters in
// Instance.spec.monitoring.destinations[].
type pmmEntryParameters struct {
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// MonitoringIntegrations implements controller.MonitoringIntegrationsProvider.
func (p *PXCProvider) MonitoringIntegrations() []string {
	return []string{pmmIntegration}
}

// MonitoringSources implements controller.MonitoringSourcesProvider. It
// publishes HAProxy's own endpoint and, while the engine pods carry the
// mysqld_exporter sidecar, the exporter's, each while a pod of its component
// is ready.
func (p *PXCProvider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	sources := &corev1alpha1.MonitoringSources{}
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(pxc, c.Name()); err != nil {
		if apierrors.IsNotFound(err) {
			return sources, nil
		}
		return nil, fmt.Errorf("get PerconaXtraDBCluster: %w", err)
	}

	if hasMetricsExporter(pxc) && pxc.Status.PXC.Ready > 0 {
		sources.Metrics = append(sources.Metrics, corev1alpha1.MetricsEndpoint{
			Component: common.ComponentEngine,
			Kind:      "mysql",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: common.ComponentEngine,
			},
			Port:   intstr.FromString(metricsPortName),
			Path:   metricsPath,
			Scheme: "http",
		})
	}
	if pxc.Spec.HAProxyEnabled() && pxc.Status.HAProxy.Ready > 0 {
		sources.Metrics = append(sources.Metrics, corev1alpha1.MetricsEndpoint{
			Component: common.ComponentProxy,
			Kind:      "haproxy",
			PodSelector: map[string]string{
				controller.InstanceLabel:  c.Name(),
				controller.ComponentLabel: common.ComponentProxy,
			},
			Port:   intstr.FromInt32(haproxyMetricsPort),
			Path:   metricsPath,
			Scheme: "http",
		})
	}
	return sources, nil
}

// applyMonitoring renders what the Instance's monitoring destinations ask
// for: PMM from its binding, and the exporter sidecar while a class needs
// metrics.
func applyMonitoring(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, providerSpec *corev1alpha1.ProviderSpec) error {
	live := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(live, c.Name()); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get PerconaXtraDBCluster: %w", err)
		}
		live = nil
	}

	required, err := c.RequiredSources()
	if err != nil {
		return fmt.Errorf("resolve required monitoring sources: %w", err)
	}
	if err := applyPMM(c, pxc, live, required.Integrations[pmmIntegration]); err != nil {
		return err
	}
	return applyMetricsExporter(pxc, live, required.Metrics, providerSpec)
}

// applyPMM renders spec.pmm from the Instance's pmm binding, turns PMM off once
// no entry asks for it, and keeps the live spec.pmm while that is unresolved
// (hold rule). A binding whose inputs cannot be rendered is reported on the
// binding and keeps the live spec.pmm too; only API errors fail Sync.
func applyPMM(c *controller.Context, pxc, live *pxcv1.PerconaXtraDBCluster, required controller.Requirement) error {
	if !required.Else(live != nil && live.Spec.PMM != nil && live.Spec.PMM.Enabled) {
		pxc.Spec.PMM = nil
		return syncPMMServerToken(c, pxc.Spec.SecretsName, nil)
	}
	if required != controller.Required {
		keepLivePMM(pxc, live)
		return nil
	}

	bindings, err := c.MonitoringBindings()
	if err != nil {
		return fmt.Errorf("list monitoring bindings: %w", err)
	}
	for _, rb := range bindings {
		if !rb.Apply || rb.Integration() != pmmIntegration {
			continue
		}
		credentials, err := pmmCredentials(c, rb.Destination)
		if err != nil {
			return err
		}
		pmm, token, err := renderPMM(c, rb, credentials)
		if err != nil {
			c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
			keepLivePMM(pxc, live)
			return nil
		}
		pxc.Spec.PMM = pmm
		return syncPMMServerToken(c, pxc.Spec.SecretsName, token)
	}
	keepLivePMM(pxc, live)
	return nil
}

func keepLivePMM(pxc, live *pxcv1.PerconaXtraDBCluster) {
	pxc.Spec.PMM = nil
	if live != nil && live.Spec.PMM != nil {
		pxc.Spec.PMM = live.Spec.PMM.DeepCopy()
	}
}

// pmmCredentials returns the destination's credentials Secret, nil when the
// destination names none or the Secret does not exist.
func pmmCredentials(c *controller.Context, destination *monitoringv1alpha1.MonitoringDestination) (*corev1.Secret, error) {
	if destination.Spec.CredentialsSecretRef == nil {
		return nil, nil
	}
	secret := &corev1.Secret{}
	if err := c.Get(secret, destination.Spec.CredentialsSecretRef.Name); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get credentials Secret of MonitoringDestination %s: %w", destination.Name, err)
	}
	return secret, nil
}

// renderPMM builds spec.pmm for an Apply-able pmm binding and returns the PMM
// server token to mirror. Errors describe an input the user has to fix.
func renderPMM(c *controller.Context, rb controller.ResolvedBinding, credentials *corev1.Secret) (*pxcv1.PMMSpec, []byte, error) {
	destination := pmmDestinationParameters{}
	if rb.Destination.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Destination.Spec.Parameters.Raw, &destination); err != nil {
			return nil, nil, fmt.Errorf("decode MonitoringDestination %s parameters: %w", rb.Destination.Name, err)
		}
	}
	serverHost, err := pmmServerHostFromURL(destination.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("MonitoringDestination %s parameters.url: %w", rb.Destination.Name, err)
	}

	entry := pmmEntryParameters{}
	if rb.Binding.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Binding.Spec.Parameters.Raw, &entry); err != nil {
			return nil, nil, fmt.Errorf("decode parameters of destination entry %s: %w", rb.Entry.Name, err)
		}
	}

	resources, err := pmmResources(c.Instance().Spec.Components[common.ComponentEngine].Resources, entry.Resources)
	if err != nil {
		return nil, nil, fmt.Errorf("parameters of destination entry %s: %w", rb.Entry.Name, err)
	}

	if rb.Destination.Spec.CredentialsSecretRef == nil {
		return nil, nil, fmt.Errorf("MonitoringDestination %s has no credentialsSecretRef", rb.Destination.Name)
	}
	if credentials == nil {
		return nil, nil, fmt.Errorf("credentials Secret %s of MonitoringDestination %s not found",
			rb.Destination.Spec.CredentialsSecretRef.Name, rb.Destination.Name)
	}
	token := credentials.Data[pmmAPIKeyKey]
	if len(token) == 0 {
		return nil, nil, fmt.Errorf("credentials Secret %s has no %s", credentials.Name, pmmAPIKeyKey)
	}

	return &pxcv1.PMMSpec{
		Enabled:           true,
		ServerHost:        serverHost,
		Image:             rb.Class.Spec.ProviderManaged.AgentImage,
		CustomClusterName: c.Name(),
		ImagePullPolicy:   corev1.PullIfNotPresent,
		Resources:         resources,
		PxcParams:         pmmAdminParams(c, ""),
		// The operator hard-codes the pmm-admin params of HAProxy, so only
		// ProxySQL can carry the instance labels.
		ProxysqlParams: pmmAdminParams(c, common.ComponentProxy),
	}, token, nil
}

var (
	_ controller.MonitoringSourcesProvider      = (*PXCProvider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*PXCProvider)(nil)
)
