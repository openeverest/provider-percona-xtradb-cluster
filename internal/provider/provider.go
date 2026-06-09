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
	"fmt"
	"net"
	"net/url"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// defaultSpec returns the default PerconaXtraDBClusterSpec for new instances.
func defaultSpec() pxcv1.PerconaXtraDBClusterSpec {
	return pxcv1.PerconaXtraDBClusterSpec{
		UpdateStrategy: pxcv1.SmartUpdateStatefulSetStrategyType,
		UpgradeOptions: pxcv1.UpgradeOptions{
			Apply:    "disabled",
			Schedule: "0 4 * * *",
		},
		VolumeExpansionEnabled: true,
		CRVersion:              "1.20.0",
		PXC: &pxcv1.PXCSpec{
			PodSpec: &pxcv1.PodSpec{
				VolumeSpec: &pxcv1.VolumeSpec{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("6Gi"),
							},
						},
					},
				},
				Size: 3,
			},
		},
		HAProxy: &pxcv1.HAProxySpec{
			PodSpec: pxcv1.PodSpec{
				Enabled: true,
				Size:    2,
			},
		},
	}
}

func imageForBundledComponent(c *controller.Context, spec *corev1alpha1.ProviderSpec, componentName string) (string, error) {
	selectedBundle := c.Instance().Spec.Version
	if selectedBundle == "" {
		selectedBundle = c.Instance().Status.Version
	}
	if selectedBundle == "" {
		selectedBundle = controller.GetDefaultVersionBundleName(spec)
	}
	if selectedBundle != "" {
		bundle, err := controller.ResolveVersionBundle(spec, selectedBundle)
		if err != nil {
			return "", err
		}
		if componentVersion, ok := bundle.Components[componentName]; ok {
			if image := controller.GetImageForVersion(spec, componentName, componentVersion); image != "" {
				return image, nil
			}
		}
	}

	return controller.GetDefaultImageForComponent(spec, componentName), nil
}

func activeProxyComponent(pxcSpec *pxcv1.PerconaXtraDBClusterSpec) (string, error) {
	if pxcSpec.HAProxyEnabled() && pxcSpec.ProxySQLEnabled() {
		return "", fmt.Errorf("can't enable both HAProxy and ProxySQL please only select one of them")
	}
	if pxcSpec.ProxySQLEnabled() {
		return common.ComponentProxySQL, nil
	}
	if pxcSpec.HAProxyEnabled() {
		return common.ComponentHAProxy, nil
	}
	return "", nil
}

func componentConfigured(component corev1alpha1.ComponentSpec) bool {
	return component.Replicas != nil ||
		component.Version != "" ||
		component.Image != "" ||
		component.Storage != nil ||
		component.Resources != nil ||
		component.Config != nil ||
		component.CustomSpec != nil
}

// ValidatePXC validates the Instance spec for PXC.
func ValidatePXC(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating PXC cluster", "cluster", c.Name())

	return nil
}

// SyncPXC creates or updates the PerconaXtraDBCluster resource based on the Instance spec.
func SyncPXC(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing PXC cluster", "cluster", c.Name())

	defer l.Info("PXC cluster synced", "cluster", c.Name())

	meta := c.ObjectMeta(c.Name())
	meta.Finalizers = []string{
		"percona.com/delete-pxc-pods-in-order",
		"percona.com/delete-pxc-pvc",
	}
	pxc := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: meta,
		Spec:       defaultSpec(),
	}

	// Get the engine component spec
	engine := c.Instance().Spec.Components[common.ComponentEngine]
	// No need to check if engine is nil, it is guaranteed to be present by the validator
	pxc.Spec.Unsafe = unsafeFlags(engine.Replicas)
	pxc.Spec.PXC.Size = *engine.Replicas

	haproxyComponent, hasHAProxy := c.Instance().Spec.Components[common.ComponentHAProxy]
	proxySQLComponent, hasProxySQL := c.Instance().Spec.Components[common.ComponentProxySQL]
	haproxySelected := hasHAProxy && componentConfigured(haproxyComponent)
	proxySQLSelected := hasProxySQL && componentConfigured(proxySQLComponent)
	if haproxySelected && proxySQLSelected {
		return fmt.Errorf("can't enable both HAProxy and ProxySQL please only select one of them")
	}

	if proxySQLSelected {
		proxySQLSize := int32(2)
		if proxySQLComponent.Replicas != nil {
			proxySQLSize = *proxySQLComponent.Replicas
		}
		pxc.Spec.HAProxy = nil
		pxc.Spec.ProxySQL = &pxcv1.ProxySQLSpec{
			PodSpec: pxcv1.PodSpec{
				Enabled: true,
				Size:    proxySQLSize,
				VolumeSpec: &pxcv1.VolumeSpec{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		}
	} else {
		haproxySize := int32(2)
		if haproxySelected && haproxyComponent.Replicas != nil {
			haproxySize = *haproxyComponent.Replicas
		}
		if pxc.Spec.HAProxy == nil {
			pxc.Spec.HAProxy = &pxcv1.HAProxySpec{}
		}
		pxc.Spec.HAProxy.Enabled = true
		pxc.Spec.HAProxy.Size = haproxySize
		pxc.Spec.ProxySQL = nil
	}

	spec, err := c.ProviderSpec()
	if err != nil {
		return err
	}

	if engine.Config == nil {
		switch engine.Replicas {
		case pointer.Int32(1):
			pxc.Spec.PXC.Configuration = pxcConfigSizeSmall
		case pointer.Int32(3):
			pxc.Spec.PXC.Configuration = pxcConfigSizeMedium
		default:
			pxc.Spec.PXC.Configuration = pxcConfigSizeLarge
		}
	}

	// Set the image: use the user-specified image if provided, otherwise resolve
	// from the version bundle (engine.Version is populated by the provider-runtime)
	// or fall back to the provider's default image.
	if engine.Image != "" {
		// User explicitly specified an image override.
		pxc.Spec.PXC.Image = engine.Image
	} else {
		if engine.Version != "" {
			pxc.Spec.PXC.Image = controller.GetImageForVersion(spec, common.ComponentEngine, engine.Version)
		}
		if pxc.Spec.PXC.Image == "" {
			pxc.Spec.PXC.Image = controller.GetDefaultImageForComponent(spec, common.ComponentEngine)
		}
	}
	pxc.Spec.PXC.ImagePullPolicy = corev1.PullIfNotPresent

	activeProxy, err := activeProxyComponent(&pxc.Spec)
	if err != nil {
		return err
	}
	if activeProxy != "" {
		proxyImage, err := imageForBundledComponent(c, spec, activeProxy)
		if err != nil {
			return err
		}
		switch activeProxy {
		case common.ComponentHAProxy:
			pxc.Spec.HAProxy.Image = proxyImage
		case common.ComponentProxySQL:
			pxc.Spec.ProxySQL.Image = proxyImage
		}
	}

	usersSecretName := "everest-secrets-" + c.Name()

	pxc.Spec.SecretsName = usersSecretName

	if err := c.Apply(pxc); err != nil {
		return err
	}

	if c.Instance().Spec.DataSource != nil {
		current := &pxcv1.PerconaXtraDBCluster{}
		if err := c.Get(current, c.Name()); err != nil {
			// Cluster has not been created yet (first Sync). The next
			// reconcile after the PXC CR appears will re-enter this branch.
			return nil
		}
		if _, err := c.ReconcileDataSource(); err != nil {
			return fmt.Errorf("reconcile data source: %w", err)
		}
	}

	return nil
}

// unsafeFlags returns pxcv1.UnsafeFlags considering the given replicas configuration.
func unsafeFlags(replicas *int32) pxcv1.UnsafeFlags {
	const productionSafeReplsetSize = 3
	if replicas != nil && *replicas < productionSafeReplsetSize {
		return pxcv1.UnsafeFlags{PXCSize: true}
	}

	return pxcv1.UnsafeFlags{}
}

// StatusPXC computes the current status of the PXC cluster.
func StatusPXC(c *controller.Context) (controller.Status, error) {
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(pxc, c.Name()); err != nil {
		return controller.Provisioning("Waiting for PerconaXtraDBCluster"), nil
	}
	if ds := c.GetDataSourceStatus(); ds != nil && !ds.Done {
		return controller.Restoring(ds.Message), nil
	}
	switch pxc.Status.Status {
	case pxcv1.AppStateReady:
		details, err := buildConnectionDetails(c, pxc)
		if err != nil {
			return controller.Failed("Failed to build connection details: " + err.Error()), nil
		}
		return controller.ReadyWithConnectionDetails(details), nil
	case pxcv1.AppStateError:
		return controller.Failed(strings.Join(pxc.Status.Messages, ";")), nil
	default:
		return controller.Provisioning("Cluster is being created"), nil
	}
}

// CleanupPXC handles deletion of the PXC cluster.
func CleanupPXC(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up PXC cluster", "cluster", c.Name())

	// TODO: Implemenent handling of finalizers
	pxc := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: c.ObjectMeta(c.Name()),
	}
	if err := c.Delete(pxc); err != nil {
		return err
	}

	l.Info("PXC cluster cleaned up", "cluster", c.Name())

	return nil
}

// PXCProvider implements the ProviderInterface.
type PXCProvider struct {
	controller.BaseProvider
	client client.Client
}

// SetClient injects the Kubernetes client into the provider.
// Must be called after reconciler.New() and before r.Start().
// TODO: this is not great, change the way manager is configured
// so injection is not necessary.
func (p *PXCProvider) SetClient(c client.Client) {
	p.client = c
}

// NewPXCProviderInterface creates a new PXC provider.
// The provider name must match the Provider CR name so the runtime
// can automatically fetch schemas and version metadata from it.
// Call SetClient on the returned provider before starting the reconciler
// so the MonitoringConfig watch handler can list referencing Instances.
func NewPXCProviderInterface() *PXCProvider {
	p := &PXCProvider{}

	p.BaseProvider = controller.BaseProvider{
		ProviderName: "percona-xtradb-cluster",
		SchemeFuncs: []func(*runtime.Scheme) error{
			pxcv1.SchemeBuilder.AddToScheme,
			monitoringv1alpha1.SchemeBuilder.AddToScheme,
		},
		WatchConfigs: []controller.WatchConfig{
			// Watch owned PXC resources - only trigger on spec changes
			controller.WatchOwned(&pxcv1.PerconaXtraDBCluster{}),
		},
	}

	return p
}

// Validate validates the Instance spec.
func (p *PXCProvider) Validate(c *controller.Context) error {
	return ValidatePXC(c)
}

// Sync ensures all resources exist and are configured correctly.
func (p *PXCProvider) Sync(c *controller.Context) error {
	return SyncPXC(c)
}

// Status computes the current status of the cluster.
func (p *PXCProvider) Status(c *controller.Context) (controller.Status, error) {
	return StatusPXC(c)
}

// Cleanup handles deletion of the cluster and any necessary cleanup.
func (p *PXCProvider) Cleanup(c *controller.Context) error {
	return CleanupPXC(c)
}

// FieldIndexes implements controller.FieldIndexProvider.
// It registers indexes used by watchers.
func (p *PXCProvider) FieldIndexes() []controller.FieldIndex {
	return []controller.FieldIndex{}
}

// BackupWatches implements controller.BackupWatcher. The runtime's Backup
// reconciler watches PerconaXtraDBClusterBackup CRs as owned resources so
// operator status changes are routed directly to the parent Backup CR via
// owner-reference based enqueue (1:1, no Instance fan-out). SyncBackup sets
// the controller reference from Backup -> PerconaXtraDBClusterBackup, so
// owner-based enqueue applies to every adopted backup. Operator-emitted
// scheduled backups are still routed through the Instance reconciler (where
// they get mirrored into Backup CRs) until the next SyncBackup adopts them.
func (p *PXCProvider) BackupWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchOwned(&pxcv1.PerconaXtraDBClusterBackup{}),
	}
}

// RestoreWatches mirrors BackupWatches for PerconaXtraDBClusterRestore.
func (p *PXCProvider) RestoreWatches() []controller.WatchConfig {
	return []controller.WatchConfig{
		controller.WatchOwned(&pxcv1.PerconaXtraDBClusterRestore{}),
	}
}

// buildConnectionDetails reads the PXC Users secret and combines it with host info
// to produce a set of well-known connection details.
func buildConnectionDetails(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) (controller.ConnectionDetails, error) {
	secretName := "everest-secrets-" + c.Name()
	secret := &corev1.Secret{}
	if err := c.Get(secret, secretName); err != nil {
		return controller.ConnectionDetails{}, fmt.Errorf("failed to get credentials secret %s: %w", secretName, err)
	}

	// Adjust key names if your users secret uses different keys.
	username := "root"
	password := string(secret.Data["root"])

	host := pxc.Status.Host
	if host == "" {
		// Fallback service name pattern if status host is not populated yet.
		host = fmt.Sprintf("%s-pxc.%s.svc", c.Name(), c.Namespace())
	}
	port := "3306"

	u := &url.URL{
		Scheme: "mysql",
		Host:   net.JoinHostPort(host, port),
		Path:   "/",
		User:   url.UserPassword(username, password),
	}
	q := u.Query()
	q.Set("tls", "false")
	u.RawQuery = q.Encode()

	return controller.ConnectionDetails{
		Type:     "mysql",
		Provider: "percona-xtradb-cluster",
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		URI:      u.String(),
	}, nil
}

// Compile-time interface checks
var _ controller.ProviderInterface = (*PXCProvider)(nil)
var _ controller.WatchProvider = (*PXCProvider)(nil)
var _ controller.FieldIndexProvider = (*PXCProvider)(nil)
var _ controller.BackupProvider = (*PXCProvider)(nil)
var _ controller.BackupWatcher = (*PXCProvider)(nil)
var _ controller.RestoreWatcher = (*PXCProvider)(nil)
var _ controller.BackupMirror = (*PXCProvider)(nil)
