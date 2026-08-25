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
	"fmt"
	"net"
	"net/url"
	"strings"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/definition/components"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

func imageForComponentTypeVersion(spec *corev1alpha1.ProviderSpec, componentType, version string) string {
	ct, ok := spec.ComponentTypes[componentType]
	if !ok {
		return ""
	}
	for _, v := range ct.Versions {
		if v.Version == version {
			return v.Image
		}
	}
	return ""
}

func defaultImageForComponentType(spec *corev1alpha1.ProviderSpec, componentType string) string {
	ct, ok := spec.ComponentTypes[componentType]
	if !ok {
		return ""
	}
	for _, v := range ct.Versions {
		if v.Default && v.Image != "" {
			return v.Image
		}
	}
	for _, v := range ct.Versions {
		if v.Image != "" {
			return v.Image
		}
	}
	return ""
}

func backupImageForEngineVersion(spec *corev1alpha1.ProviderSpec, engineVersion string) string {
	if spec == nil || engineVersion == "" {
		return ""
	}

	for _, bundle := range spec.Versions {
		if bundle.Components[common.ComponentEngine] != engineVersion {
			continue
		}
		backupVersion := bundle.Components[common.ComponentBackup]
		if backupVersion == "" {
			continue
		}
		if image := controller.GetImageForVersion(spec, common.ComponentBackup, backupVersion); image != "" {
			return image
		}
	}

	return defaultImageForComponentType(spec, common.ComponentBackup)
}

func imageForBundledProxy(c *controller.Context, spec *corev1alpha1.ProviderSpec, proxyType string) (string, error) {
	selectedBundle := controller.EffectiveVersionBundleName(spec, c.Instance())
	if selectedBundle != "" {
		bundle, err := controller.ResolveVersionBundle(spec, selectedBundle)
		if err != nil {
			return "", err
		}
		if componentVersion, ok := bundle.Components[common.ComponentProxy]; ok {
			if image := imageForComponentTypeVersion(spec, proxyType, componentVersion); image != "" {
				return image, nil
			}
		}
	}

	return defaultImageForComponentType(spec, proxyType), nil
}

// ValidatePXC validates the Instance spec for PXC.
func ValidatePXC(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Validating PXC cluster", "cluster", c.Name())

	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok || engine.Replicas == nil {
		return fmt.Errorf("instance spec missing %q component replicas", common.ComponentEngine)
	}
	if *engine.Replicas < 1 {
		return fmt.Errorf("%q replicas must be >= 1", common.ComponentEngine)
	}

	proxy, ok := c.Instance().Spec.Components[common.ComponentProxy]
	if !ok {
		return fmt.Errorf("instance spec missing %q component", common.ComponentProxy)
	}
	if proxy.Type == "" {
		return fmt.Errorf("instance spec missing %q component type", common.ComponentProxy)
	}
	if proxy.Replicas == nil {
		return fmt.Errorf("instance spec missing %q component replicas", common.ComponentProxy)
	}
	if *proxy.Replicas < 1 {
		return fmt.Errorf("%q replicas must be >= 1", common.ComponentProxy)
	}
	switch proxy.Type {
	case common.ProxyTypeHAProxy, common.ProxyTypeProxySQL:
	default:
		return fmt.Errorf("unsupported proxy type %q", proxy.Type)
	}

	if c.Instance().Spec.Backup != nil && c.Instance().Spec.Backup.Enabled {
		bc, err := c.BackupClassForInstance()
		if err != nil {
			return err
		}
		if err := controller.ValidateInstanceBackupAgainstClass(c.Instance(), bc); err != nil {
			return err
		}

		pitrEnabled := 0
		for _, s := range c.Instance().Spec.Backup.Storages {
			if s.PITR != nil && s.PITR.Enabled {
				pitrEnabled++

				var rawCfg []byte
				if s.PITR.Parameters != nil {
					rawCfg = s.PITR.Parameters.Raw
				}
				if _, err := decodeAndValidatePITRConfig(s.StorageRef.Name, rawCfg); err != nil {
					return err
				}
			}
		}
		if pitrEnabled > 1 {
			return fmt.Errorf("PXC supports PITR on at most one storage")
		}
	}

	return nil
}

// SyncPXC creates or updates the PerconaXtraDBCluster resource based on the Instance spec.
func SyncPXC(c *controller.Context) error {
	l := log.FromContext(c.Context())
	l.Info("Syncing PXC cluster", "cluster", c.Name())

	defer l.Info("PXC cluster synced", "cluster", c.Name())

	activeRestore, err := hasActiveRestoreForInstance(c, c.Namespace(), c.Name())
	if err != nil {
		return fmt.Errorf("check active restores for %q: %w", c.Name(), err)
	}
	if activeRestore {
		l.Info("Skipping PXC spec sync while restore is active", "cluster", c.Name())
		return nil
	}

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
	engine, ok := c.Instance().Spec.Components[common.ComponentEngine]
	if !ok || engine.Replicas == nil {
		return fmt.Errorf("instance spec missing %q component replicas", common.ComponentEngine)
	}
	pxc.Spec.PXC.Size = *engine.Replicas

	proxy, ok := c.Instance().Spec.Components[common.ComponentProxy]
	if !ok || proxy.Type == "" || proxy.Replicas == nil {
		return fmt.Errorf("instance spec has invalid %q component; this should be caught by ValidatePXC", common.ComponentProxy)
	}

	proxyType := proxy.Type
	proxyReplicas := *proxy.Replicas

	if proxyType == common.ProxyTypeProxySQL {
		pxc.Spec.HAProxy = nil
		pxc.Spec.ProxySQL = &pxcv1.ProxySQLSpec{
			PodSpec: pxcv1.PodSpec{
				Enabled: true,
				Size:    proxyReplicas,
				VolumeSpec: &pxcv1.VolumeSpec{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		}
	} else {
		if pxc.Spec.HAProxy == nil {
			pxc.Spec.HAProxy = &pxcv1.HAProxySpec{}
		}
		pxc.Spec.HAProxy.Enabled = true
		pxc.Spec.HAProxy.Size = proxyReplicas
		pxc.Spec.ProxySQL = nil
	}

	var proxyReplicasPtr *int32
	if pxc.Spec.ProxySQLEnabled() {
		proxyReplicasPtr = &pxc.Spec.ProxySQL.Size
	}
	if pxc.Spec.HAProxyEnabled() {
		proxyReplicasPtr = &pxc.Spec.HAProxy.Size
	}
	pxc.Spec.Unsafe = unsafeFlags(engine.Replicas, proxyReplicasPtr)

	spec, err := c.ProviderSpec()
	if err != nil {
		return err
	}

	// The engine configuration file is carried inside the engine component's
	// parameters; `configuration` is the conventional property name for it.
	var engineParams components.PXCParameters
	c.TryDecodeComponentParameters(engine, &engineParams)
	if engineParams.Configuration != "" {
		pxc.Spec.PXC.Configuration = engineParams.Configuration
	} else {
		switch *engine.Replicas {
		case 1:
			pxc.Spec.PXC.Configuration = pxcConfigSizeSmall
		case 3:
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

	if proxyType != "" {
		proxyImage := ""
		if proxy.Image != "" {
			proxyImage = proxy.Image
		} else if proxy.Version != "" {
			proxyImage = imageForComponentTypeVersion(spec, proxyType, proxy.Version)
		}
		if proxyImage == "" {
			proxyImage, err = imageForBundledProxy(c, spec, proxyType)
			if err != nil {
				return err
			}
		}
		switch proxyType {
		case common.ProxyTypeHAProxy:
			pxc.Spec.HAProxy.Image = proxyImage
		case common.ProxyTypeProxySQL:
			pxc.Spec.ProxySQL.Image = proxyImage
		default:
			return fmt.Errorf("unsupported proxy type %q", proxyType)
		}
	}

	if err := applyMonitoringSettings(c, pxc, spec); err != nil {
		return err
	}

	if err := applyBackupSettings(c, pxc); err != nil {
		return err
	}

	usersSecretName := "everest-secrets-" + c.Name()

	pxc.Spec.SecretsName = usersSecretName

	// When seeding from a DataSource, the target cluster's users secret must
	// contain the same credentials as the source cluster: the restored datadir
	// carries the source cluster's mysql user table, so a freshly generated
	// secret would leave the operator, proxy and monitoring users unable to
	// authenticate. Copy BEFORE applying the PXC CR so the operator never
	// initializes the secret with random passwords.
	if c.Instance().Spec.DataSource != nil {
		if err := ensureDataSourceCredentials(c, usersSecretName); err != nil {
			return err
		}
	}

	if err := c.Apply(pxc); err != nil {
		return err
	}

	// Initial seeding from .spec.dataSource: gated on the engine being Ready.
	// The PXC restore job mounts the datadir PVC of the first engine pod and
	// reads the cluster's users secret, and a failed validation is terminal for
	// the PerconaXtraDBClusterRestore, so issuing it before the StatefulSet
	// exists fails the restore permanently. While the gate is not satisfied the
	// helper is not invoked and StatusPXC reports Restoring so callers know the
	// Instance is still being seeded.
	if c.Instance().Spec.DataSource != nil {
		current := &pxcv1.PerconaXtraDBCluster{}
		if err := c.Get(current, c.Name()); err != nil {
			// Cluster has not been created yet (first Sync). The next
			// reconcile after the PXC CR appears will re-enter this branch.
			return nil
		}
		if current.Status.Status != pxcv1.AppStateReady {
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    false,
				State:   controller.DataSourceStateWaiting,
				Reason:  corev1alpha1.ReasonDataSourceWaitingForCluster,
				Message: "waiting for PerconaXtraDBCluster to be Ready",
			})
			return nil
		}
		if _, err := c.ReconcileDataSource(); err != nil {
			return fmt.Errorf("reconcile data source: %w", err)
		}
	}

	return nil
}

// ensureDataSourceCredentials copies the users secret from the source Instance
// to the target Instance when seeding from .spec.dataSource.
// This is idempotent: if the target secret already exists it is not
// overwritten, ensuring reconcile loops don't corrupt credentials.
func ensureDataSourceCredentials(c *controller.Context, targetSecretName string) error {
	// If the target secret already exists, we're done. Either a previous
	// reconcile created it or the user pre-provisioned it manually.
	targetSecret := &corev1.Secret{}
	if err := c.Get(targetSecret, targetSecretName); err == nil {
		return nil
	}

	sourceInstanceName, err := dataSourceInstanceName(c)
	if err != nil {
		return err
	}
	if sourceInstanceName == "" {
		// The source cannot be resolved yet (or at all); ReconcileDataSource
		// surfaces that as a condition. Let Sync continue — the gate on the
		// cluster being Ready holds the restore.
		return nil
	}

	// The source Instance's users secret follows the same naming convention.
	sourceSecretName := "everest-secrets-" + sourceInstanceName
	sourceSecret := &corev1.Secret{}
	if err := c.Get(sourceSecret, sourceSecretName); err != nil {
		if apierrors.IsNotFound(err) {
			message := fmt.Sprintf("source Instance credentials secret %q not found; the source Instance may have been deleted", sourceSecretName)
			c.SetDataSourceStatus(controller.DataSourceStatus{
				Done:    true,
				State:   controller.DataSourceStateFailed,
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: message,
			})
			return &controller.DataSourceConfigError{
				Reason:  corev1alpha1.ReasonDataSourceFailed,
				Message: message,
			}
		}
		return fmt.Errorf("get source credentials secret %q: %w", sourceSecretName, err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      targetSecretName,
			Namespace: c.Namespace(),
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "everest",
				"app.kubernetes.io/instance":   c.Name(),
			},
		},
		Data: sourceSecret.Data,
	}
	if err := c.Client().Create(c.Context(), newSecret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Race: another reconcile beat us to it.
			return nil
		}
		return fmt.Errorf("create target credentials secret %q: %w", targetSecretName, err)
	}
	return nil
}

// dataSourceInstanceName resolves the Instance whose data is being seeded from.
// A "Backup" source names it indirectly, through the referenced Backup CR's
// .spec.instanceRef; a "PointInTime" source names it directly (required by a
// CEL rule on Instance, since a new Instance has no stream of its own to
// default to). An empty name means the source is not resolvable yet.
func dataSourceInstanceName(c *controller.Context) (string, error) {
	ds := c.Instance().Spec.DataSource
	switch ds.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		if ds.Backup == nil || ds.Backup.BackupRef.Name == "" {
			return "", nil
		}
		srcBackup := &backupv1alpha1.Backup{}
		if err := c.Get(srcBackup, ds.Backup.BackupRef.Name); err != nil {
			if apierrors.IsNotFound(err) {
				return "", nil
			}
			return "", fmt.Errorf("get source Backup %q for credential copy: %w", ds.Backup.BackupRef.Name, err)
		}
		return srcBackup.Spec.InstanceRef.Name, nil
	case backupv1alpha1.DataSourceTypePointInTime:
		if ds.PointInTime == nil || ds.PointInTime.Source.InstanceRef == nil {
			return "", nil
		}
		return ds.PointInTime.Source.InstanceRef.Name, nil
	default:
		return "", nil
	}
}

// unsafeFlags returns pxcv1.UnsafeFlags considering the given replicas configuration.
func unsafeFlags(replicas, proxyReplicas *int32) pxcv1.UnsafeFlags {
	const productionSafeReplsetSize = 3
	const productionSafeProxySize = 2

	flags := pxcv1.UnsafeFlags{}
	if replicas != nil && *replicas < productionSafeReplsetSize {
		flags.PXCSize = true
	}
	if proxyReplicas != nil && *proxyReplicas < productionSafeProxySize {
		flags.ProxySize = true
	}

	return flags
}

// StatusPXC computes the current status of the PXC cluster.
func StatusPXC(c *controller.Context) (controller.Status, error) {
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(pxc, c.Name()); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return controller.Provisioning("Waiting for PerconaXtraDBCluster"), nil
		}
		return controller.Failed("Failed to get PerconaXtraDBCluster: " + err.Error()), err
	}
	if ds := c.GetDataSourceStatus(); ds != nil && !ds.Done {
		return controller.Restoring(ds.Message), nil
	}

	// A restore drives the engine through paused, starting and momentarily
	// ready states, so reading the phase off the engine alone makes the Instance
	// flap between Restoring, Provisioning and Ready while one is in flight.
	// The Restore CR reaching a terminal state is what ends the restore, so let
	// it own the phase for as long as it is running.
	activeRestore, err := hasActiveRestoreForInstance(c, c.Namespace(), c.Name())
	if err != nil {
		return controller.Status{}, err
	}
	if activeRestore {
		return controller.Restoring("Restore is running"), nil
	}

	switch pxc.Status.Status {
	case pxcv1.AppStatePaused:
		return controller.Provisioning("Cluster is paused"), nil
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

	// TODO: Implement handling of finalizers
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
			controller.WatchExternal(
				&monitoringv1alpha1.MonitoringConfig{},
				handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
					if p.client == nil {
						return nil
					}

					mc, ok := obj.(*monitoringv1alpha1.MonitoringConfig)
					if !ok {
						return nil
					}

					instances := &corev1alpha1.InstanceList{}
					if err := p.client.List(ctx, instances,
						client.InNamespace(mc.Namespace),
						client.MatchingFields{monitoringConfigRefFieldPath: mc.Name},
					); err != nil {
						return nil
					}

					requests := make([]reconcile.Request, 0, len(instances.Items))
					for i := range instances.Items {
						instance := instances.Items[i]
						if instance.Spec.ProviderRef.Name != p.Name() {
							continue
						}
						requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&instance)})
					}
					return requests
				}),
				controller.ResourceVersionChangedPredicate,
			),
			// Watch operator backups so the PITR window published by
			// BackupStorageStatuses refreshes as the operator stamps
			// latestRestorableTime and the PITRReady condition on them.
			// Operator backups are not owned by the Instance, so map them to
			// the parent via spec.pxcCluster.
			controller.WatchExternal(&pxcv1.PerconaXtraDBClusterBackup{},
				handler.EnqueueRequestsFromMapFunc(enqueueOperatorBackupInstance()),
			),
			// Watch Restores so the Instance leaves the Restoring phase as soon
			// as one reaches a terminal state. The engine usually reports ready
			// before the Restore CR does, so without this the Instance would sit
			// in Restoring until an unrelated event arrived.
			controller.WatchExternal(&backupv1alpha1.Restore{},
				handler.EnqueueRequestsFromMapFunc(enqueueRestoreInstance()),
			),
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
	return []controller.FieldIndex{
		{
			Object:    &corev1alpha1.Instance{},
			FieldPath: monitoringConfigRefFieldPath,
			Extractor: func(obj client.Object) []string {
				instance, ok := obj.(*corev1alpha1.Instance)
				if !ok {
					return nil
				}

				monitoringComponent, ok := instance.Spec.Components[common.ComponentMonitoring]
				if !ok {
					return nil
				}

				monitoringConfigName, err := monitoringConfigNameFromComponent(monitoringComponent)
				if err != nil || monitoringConfigName == "" {
					return nil
				}

				return []string{monitoringConfigName}
			},
		},
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
	passBytes, ok := secret.Data["root"]
	if !ok {
		return controller.ConnectionDetails{}, fmt.Errorf("credentials secret %s missing %q key", secretName, "root")
	}
	password := string(passBytes)
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
