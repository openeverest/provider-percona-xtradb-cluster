package provider

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	common "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Compile-time interface checks.
var _ controller.BackupMirror = (*PXCProvider)(nil)

const (
	backupTypeCron                = "cron"
	defaultPITRTimeBetweenUploads = 60.0
	defaultPITRTimeoutSeconds     = 3600.0
)

// Mirror implements controller.BackupMirror (optional). The runtime invokes
// Mirror() for operator backup events. Return a Backup CR to create it
// idempotently, or nil to skip (on-demand backups, missing Instance, or backups
// when Instance has no backup configuration).
func (p *PXCProvider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	pxcBackup, ok := obj.(*pxcv1.PerconaXtraDBClusterBackup)
	if !ok {
		return nil, fmt.Errorf("unexpected operator backup type %T", obj)
	}

	if !pxcBackup.DeletionTimestamp.IsZero() {
		return nil, nil
	}

	scheduleName, isScheduled := scheduledBackupName(pxcBackup)
	if !isScheduled {
		return nil, nil
	}

	for _, owner := range pxcBackup.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == backupv1alpha1.GroupVersion.String() && owner.Kind == "Backup" {
			return nil, nil
		}
	}

	instance := &corev1alpha1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: pxcBackup.Namespace, Name: pxcBackup.Spec.PXCCluster}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get instance %q: %w", pxcBackup.Spec.PXCCluster, err)
	}
	if scheduleName == "" {
		scheduleName = scheduleNameFromAncestor(pxcBackup.Namespace, pxcBackup.Spec.PXCCluster, pxcBackup.Labels[naming.LabelPerconaBackupAncestorName])
	}
	if instance.Spec.ProviderRef.Name != "percona-xtradb-cluster" || instance.Spec.Backup == nil || instance.Spec.Backup.ClassRef.Name == "" {
		return nil, nil
	}

	storageName := pxcBackup.Status.StorageName
	if storageName == "" {
		storageName = pxcBackup.Spec.StorageName
	}
	if storageName == "" {
		return nil, nil
	}

	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pxcBackup.Name,
			Namespace: pxcBackup.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: pxcv1.SchemeGroupVersion.String(),
				Kind:       "PerconaXtraDBClusterBackup",
				Name:       pxcBackup.Name,
				UID:        pxcBackup.UID,
			}},
		},
		Spec: backupv1alpha1.BackupSpec{
			Origin: backupv1alpha1.BackupOrigin{
				Type:        backupv1alpha1.BackupOriginTypeInstance,
				InstanceRef: &common.ObjectRef{Name: pxcBackup.Spec.PXCCluster},
			},
			ClassRef:     common.ObjectRef{Name: instance.Spec.Backup.ClassRef.Name},
			StorageRef:   common.ObjectRef{Name: storageName},
			ScheduleName: scheduleName,
		},
	}, nil
}

func scheduledBackupName(opBackup *pxcv1.PerconaXtraDBClusterBackup) (string, bool) {
	if name := opBackup.SchedulerName; name != "" {
		return name, true
	}
	if opBackup.Labels[naming.LabelPerconaBackupType] != backupTypeCron {
		return "", false
	}
	return scheduleNameFromAncestor(opBackup.Namespace, opBackup.Spec.PXCCluster, opBackup.Labels[naming.LabelPerconaBackupAncestorName]), true
}

func scheduleNameFromAncestor(namespace, clusterName, ancestor string) string {
	if ancestor == "" {
		return ""
	}
	prefix := scheduledBackupPrefix(namespace, clusterName) + "-"
	if strings.HasPrefix(ancestor, prefix) {
		return strings.TrimPrefix(ancestor, prefix)
	}
	return ancestor
}

func scheduledBackupPrefix(namespace, clusterName string) string {
	if namespace == "" || clusterName == "" {
		return ""
	}
	h := sha1.Sum([]byte(namespace + "-" + clusterName))
	return hex.EncodeToString(h[:])[:5]
}

func applyBackupSettings(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) error {
	if c.Instance().Spec.Backup == nil || !c.Instance().Spec.Backup.Enabled {
		pxc.Spec.Backup = nil
		return nil
	}

	backupClass, err := c.BackupClassForInstance()
	if err != nil {
		return &controller.BackupConfigError{Reason: "BackupClassLookupFailed", Message: err.Error()}
	}
	if err := controller.ValidateInstanceBackupAgainstClass(c.Instance(), backupClass); err != nil {
		reason := "InvalidBackupConfiguration"
		if errors.Is(err, controller.ErrBackupClassLimitsExceeded) {
			reason = controller.LimitsExceededReason
		}
		return &controller.BackupConfigError{Reason: reason, Message: err.Error()}
	}

	backupSpec := &pxcv1.BackupSpec{
		Storages: make(map[string]*pxcv1.BackupStorageSpec, len(c.Instance().Spec.Backup.Storages)),
	}
	providerSpec, err := c.ProviderSpec()
	if err != nil {
		return err
	}

	selectedBundle := controller.EffectiveVersionBundleName(providerSpec, c.Instance())
	if selectedBundle == "" {
		return &controller.BackupConfigError{Reason: "BackupImageUnavailable", Message: "cannot resolve version bundle for backup image selection"}
	}

	bundle, err := controller.ResolveVersionBundle(providerSpec, selectedBundle)
	if err != nil {
		return err
	}
	backupVersion, hasBackupComponent := bundle.Components["backup"]
	if hasBackupComponent && backupVersion != "" {
		backupSpec.Image = controller.GetImageForVersion(providerSpec, "backup", backupVersion)
		if backupSpec.Image == "" {
			return &controller.BackupConfigError{Reason: "BackupImageUnavailable", Message: fmt.Sprintf("backup version %q from bundle %q is not defined in componentTypes.backup", backupVersion, selectedBundle)}
		}
	} else {
		// Keep compatibility with older bundles that did not include components.backup.
		backupSpec.Image = defaultImageForComponentType(providerSpec, "backup")
		if backupSpec.Image == "" {
			engineVersion := bundle.Components["engine"]
			backupSpec.Image = backupImageForEngineVersion(providerSpec, engineVersion)
		}
		if backupSpec.Image == "" {
			return &controller.BackupConfigError{Reason: "BackupImageUnavailable", Message: fmt.Sprintf("version bundle %q must define components.backup or provider must define a default backup image", selectedBundle)}
		}
	}

	liveBackup := &pxcv1.BackupSpec{}
	live := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(live, c.Name()); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get PerconaXtraDBCluster: %w", err)
		}
	} else if live.Spec.Backup != nil {
		liveBackup = live.Spec.Backup
	}

	storages, err := c.BackupStorages()
	if err != nil {
		return fmt.Errorf("resolve backup storages: %w", err)
	}
	for _, s := range storages {
		if s.Apply() {
			opStorage, err := renderBackupStorage(c, s)
			if err == nil {
				backupSpec.Storages[s.Name()] = opStorage
				continue
			}
			if errors.As(err, new(apierrors.APIStatus)) && !apierrors.IsNotFound(err) {
				return err
			}
			c.RejectBackupStorage(s.Name(), err)
		}
		// Hold rule: keep the storage the engine already has, never add one.
		if held, ok := liveBackup.Storages[s.Name()]; ok {
			backupSpec.Storages[s.Name()] = held.DeepCopy()
		}
	}

	pitrEnabled := 0
	for _, storage := range c.Instance().Spec.Backup.Storages {
		_, rendered := backupSpec.Storages[storage.StorageRef.Name]

		if storage.PITR != nil && storage.PITR.Enabled {
			pitrEnabled++

			var rawCfg []byte
			if storage.PITR.Parameters != nil {
				rawCfg = storage.PITR.Parameters.Raw
			}
			cfg, err := decodeAndValidatePITRConfig(storage.StorageRef.Name, rawCfg)
			if err != nil {
				return &controller.BackupConfigError{Reason: "InvalidPITRConfig", Message: err.Error()}
			}
			if rendered {
				backupSpec.PITR.Enabled = true
				backupSpec.PITR.StorageName = storage.StorageRef.Name
				if cfg.TimeBetweenUploads != nil {
					backupSpec.PITR.TimeBetweenUploads = *cfg.TimeBetweenUploads
				}
				if cfg.TimeoutSeconds != nil {
					backupSpec.PITR.TimeoutSeconds = *cfg.TimeoutSeconds
				}
			} else if _, ok := backupSpec.Storages[liveBackup.PITR.StorageName]; ok && liveBackup.PITR.Enabled {
				// The binlog stream stays where it is until the engine has the new storage.
				backupSpec.PITR = *liveBackup.PITR.DeepCopy()
			}
		}

		if !rendered {
			continue
		}
		for _, schedule := range storage.Schedules {
			if !schedule.Enabled {
				continue
			}
			retention, err := buildPXCRetention(schedule)
			if err != nil {
				return &controller.BackupConfigError{Reason: "RetentionTypeUnsupported", Message: err.Error()}
			}
			backupSpec.Schedule = append(backupSpec.Schedule, pxcv1.PXCScheduledBackupSchedule{
				Name:        schedule.Name,
				Schedule:    schedule.Cron,
				StorageName: storage.StorageRef.Name,
				Retention:   retention,
			})
		}
	}

	if len(c.Instance().Spec.Backup.Storages) == 0 {
		return &controller.BackupConfigError{Reason: "NoStoragesConfigured", Message: "spec.backup.enabled=true requires at least one storage"}
	}
	if pitrEnabled > 1 {
		return &controller.BackupConfigError{Reason: "PITRConfigurationInvalid", Message: "PXC supports PITR on at most one storage"}
	}

	pxc.Spec.Backup = backupSpec
	return nil
}

// renderBackupStorage builds the operator storage of an entry the runtime
// cleared to render. PXC speaks plain S3 to every S3 implementation.
func renderBackupStorage(c *controller.Context, s controller.ResolvedBackupStorage) (*pxcv1.BackupStorageSpec, error) {
	s3, _, err := c.BackupStorageS3(s.Storage)
	if err != nil {
		return nil, err
	}
	tls, err := c.BackupStorageTLS(s.Storage, controller.TLSSupport{CustomCA: true, SkipVerify: true})
	if err != nil {
		return nil, err
	}

	opStorage := &pxcv1.BackupStorageSpec{
		Type: pxcv1.BackupStorageS3,
		S3: &pxcv1.BackupStorageS3Spec{
			Bucket: s.Storage.Spec.Location.Bucket + "/" + s.Path,
			// The operator reads AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY from it.
			CredentialsSecret: s.Storage.Spec.Authentication.CredentialsSecretRef.Name,
			Region:            s3.Region,
			EndpointURL:       s3.EndpointURL,
		},
	}
	if tls.InsecureSkipVerify {
		opStorage.VerifyTLS = new(false)
	}
	if ref := tls.CASecretKeyRef; ref != nil {
		opStorage.S3.CABundle = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
			Key:                  ref.Key,
		}
	}
	if s3.ForcePathStyle {
		opStorage.ContainerOptions = &pxcv1.BackupContainerOptions{
			Env: []corev1.EnvVar{{
				Name:  "AWS_FORCE_PATH_STYLE",
				Value: "true",
			}},
		}
	}
	return opStorage, nil
}

// buildPXCRetention returns nil (keep all) when the schedule sets no retention.
// The PXC operator only supports count-based retention.
func buildPXCRetention(schedule corev1alpha1.InstanceBackupSchedule) (*pxcv1.PXCScheduledBackupRetention, error) {
	if schedule.Retention == nil {
		return nil, nil
	}
	if schedule.Retention.Type != corev1alpha1.BackupScheduleRetentionTypeCount {
		return nil, fmt.Errorf("schedule %q: %s retention is not supported by PXC, use count", schedule.Name, schedule.Retention.Type)
	}
	return &pxcv1.PXCScheduledBackupRetention{
		Type:              "count",
		Count:             int(*schedule.Retention.Count),
		DeleteFromStorage: true,
	}, nil
}

func decodeAndValidatePITRConfig(storageName string, raw []byte) (*pxcPITRConfig, error) {
	cfg := &pxcPITRConfig{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("decode PITR config for storage %q: %w", storageName, err)
		}
	}

	if cfg.TimeBetweenUploads == nil {
		v := defaultPITRTimeBetweenUploads
		cfg.TimeBetweenUploads = &v
	}
	if cfg.TimeoutSeconds == nil {
		v := defaultPITRTimeoutSeconds
		cfg.TimeoutSeconds = &v
	}

	if cfg.TimeBetweenUploads != nil && *cfg.TimeBetweenUploads < 1 {
		return nil, fmt.Errorf("PITR config for storage %q: timeBetweenUploads must be >= 1", storageName)
	}
	if cfg.TimeoutSeconds != nil && *cfg.TimeoutSeconds < 1 {
		return nil, fmt.Errorf("PITR config for storage %q: timeoutSeconds must be >= 1", storageName)
	}

	return cfg, nil
}

// OperatorBackupType implements controller.BackupMirror (optional).
func (p *PXCProvider) OperatorBackupType() client.Object {
	return &pxcv1.PerconaXtraDBClusterBackup{}
}
