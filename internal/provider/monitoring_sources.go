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
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// pmmIntegration is the integration name declared in the chart's monitoring
// contract and matched by the pmm MonitoringClass.
const pmmIntegration = "pmm"

// haproxyStatsPort is where the operator's HAProxy config serves
// prometheus-exporter on /metrics (operator >= 1.17.0).
const haproxyStatsPort int32 = 8404

// MonitoringIntegrations implements controller.MonitoringIntegrationsProvider.
func (p *PXCProvider) MonitoringIntegrations() []string {
	return []string{pmmIntegration}
}

// MonitoringSources implements controller.MonitoringSourcesProvider. HAProxy
// serves native OpenMetrics; the PXC engine is probed through the
// mysqld_exporter Deployment that reconcileMetricsExporter runs on demand.
func (p *PXCProvider) MonitoringSources(c *controller.Context) (*corev1alpha1.MonitoringSources, error) {
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(pxc, c.Name()); err != nil {
		// Nothing is serving before the engine CR exists.
		return &corev1alpha1.MonitoringSources{}, nil //nolint:nilerr
	}
	s := &corev1alpha1.MonitoringSources{}
	engineMetricsSources(c, pxc, s)
	if pxc.Spec.HAProxyEnabled() {
		s.Metrics = append(s.Metrics, corev1alpha1.MetricsEndpoint{
			Component: common.ComponentProxy,
			Kind:      "haproxy",
			PodSelector: map[string]string{
				"app.kubernetes.io/name":      "percona-xtradb-cluster",
				"app.kubernetes.io/instance":  c.Name(),
				"app.kubernetes.io/component": "haproxy",
			},
			Port:   corev1alpha1.MetricsEndpointPort{Number: new(haproxyStatsPort)},
			Path:   "/metrics",
			Scheme: "http",
		})
	}
	return s, nil
}

// pmmDestinationParameters is the shape of MonitoringDestination.spec.parameters
// for the pmm class.
type pmmDestinationParameters struct {
	URL       string `json:"url"`
	VerifyTLS *bool  `json:"verifyTLS,omitempty"`
}

// pmmInstanceParameters is the shape of
// Instance.spec.monitoring.destinations[].parameters for the pmm class.
type pmmInstanceParameters struct {
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// applyPMMBindings renders spec.pmm from the Instance's ProviderManaged pmm
// binding (a spec.monitoring.destinations[] entry), if any. It reports whether a binding claimed spec.pmm so the
// caller can fall back to the legacy monitoring component otherwise.
//
// Hold rule: a binding that exists but is not Accepted keeps whatever the
// live engine CR carries. A binding that fails to render is reported through
// SetMonitoringBindingResult and likewise keeps the live rendering; it never
// fails Sync.
func applyPMMBindings(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, providerSpec *corev1alpha1.ProviderSpec) (bool, error) {
	bindings, err := c.MonitoringBindings()
	if err != nil {
		return false, err
	}
	for _, rb := range bindings {
		if rb.Mode() != corev1alpha1.MonitoringExecutionModeProviderManaged || rb.Entry == nil {
			continue
		}
		if rb.Class != nil && rb.Integration() != pmmIntegration {
			continue
		}
		if !rb.Apply {
			keepLivePMM(c, pxc)
			return true, nil
		}
		if err := renderPMMBinding(c, pxc, providerSpec, rb); err != nil {
			log.FromContext(c.Context()).Error(err, "PMM binding could not be rendered", "binding", rb.Binding.Name)
			c.SetMonitoringBindingResult(rb.Binding.Name, monitoringv1alpha1.ReasonRenderFailed, err.Error())
			keepLivePMM(c, pxc)
		}
		return true, nil
	}
	return false, nil
}

func keepLivePMM(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) {
	live := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(live, c.Name()); err == nil && live.Spec.PMM != nil {
		pxc.Spec.PMM = live.Spec.PMM.DeepCopy()
	}
}

func renderPMMBinding(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, providerSpec *corev1alpha1.ProviderSpec, rb controller.ResolvedBinding) error {
	dstParams := pmmDestinationParameters{}
	if rb.Destination.Spec.Parameters != nil {
		if err := json.Unmarshal(rb.Destination.Spec.Parameters.Raw, &dstParams); err != nil {
			return fmt.Errorf("decode MonitoringDestination %q parameters: %w", rb.Destination.Name, err)
		}
	}
	serverHost, err := pmmServerHostFromURL(dstParams.URL)
	if err != nil {
		return fmt.Errorf("MonitoringDestination %q parameters.url: %w", rb.Destination.Name, err)
	}
	if rb.Destination.Spec.CredentialsSecretRef == nil {
		return fmt.Errorf("MonitoringDestination %q has no credentialsSecretRef", rb.Destination.Name)
	}

	image := rb.Class.Spec.ProviderManaged.AgentImage
	if image == "" {
		image = defaultImageForComponentType(providerSpec, common.MonitoringTypePMM)
	}
	if image == "" {
		return fmt.Errorf("MonitoringClass %q sets no agentImage and the provider has no default pmm image", rb.Class.Name)
	}

	if err := syncPMMCredentials(c, rb.Destination.Spec.CredentialsSecretRef.Name, image); err != nil {
		return err
	}

	pxc.Spec.PMM = &pxcv1.PMMSpec{
		Enabled:           true,
		ServerHost:        serverHost,
		Image:             image,
		CustomClusterName: c.Name(),
		ImagePullPolicy:   corev1.PullIfNotPresent,
	}
	if rb.Binding.Spec.Parameters != nil {
		bp := pmmInstanceParameters{}
		if err := json.Unmarshal(rb.Binding.Spec.Parameters.Raw, &bp); err != nil {
			return fmt.Errorf("decode binding parameters: %w", err)
		}
		if bp.Resources != nil {
			pxc.Spec.PMM.Resources = *bp.Resources
		}
	}
	return nil
}

var (
	_ controller.MonitoringSourcesProvider      = (*PXCProvider)(nil)
	_ controller.MonitoringIntegrationsProvider = (*PXCProvider)(nil)
)
