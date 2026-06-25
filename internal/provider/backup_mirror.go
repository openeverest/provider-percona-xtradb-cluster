package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Compile-time interface checks.
var _ controller.BackupMirror = (*PXCProvider)(nil)

// Mirror implements controller.BackupMirror (optional). The runtime invokes
// Mirror() for operator backup events. Return a Backup CR to create it
// idempotently, or nil to skip (on-demand backups, missing Instance, or backups
// when Instance has no backup configuration).
func (p *PXCProvider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	opBackup, ok := obj.(*pxcv1.PerconaXtraDBClusterBackup)
	if !ok {
		return nil, fmt.Errorf("unexpected operator backup type %T", obj)
	}

	if opBackup.SchedulerName == "" || !opBackup.DeletionTimestamp.IsZero() {
		return nil, nil
	}

	for _, owner := range opBackup.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == backupv1alpha1.GroupVersion.String() && owner.Kind == "Backup" {
			return nil, nil
		}
	}

	instance := &corev1alpha1.Instance{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: opBackup.Namespace, Name: opBackup.Spec.PXCCluster}, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get instance %q: %w", opBackup.Spec.PXCCluster, err)
	}
	if instance.Spec.Provider != "percona-xtradb-cluster" || instance.Spec.Backup == nil || instance.Spec.Backup.ClassRef.Name == "" {
		return nil, nil
	}

	storageName := opBackup.Status.StorageName
	if storageName == "" {
		storageName = opBackup.Spec.StorageName
	}
	if storageName == "" {
		return nil, nil
	}

	return &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opBackup.Name,
			Namespace: opBackup.Namespace,
		},
		Spec: backupv1alpha1.BackupSpec{
			InstanceName:    opBackup.Spec.PXCCluster,
			BackupClassName: instance.Spec.Backup.ClassRef.Name,
			StorageName:     storageName,
			ScheduleName:    opBackup.SchedulerName,
		},
	}, nil
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

	pitrEnabled := 0
	for _, storage := range c.Instance().Spec.Backup.Storages {
		if storage.StorageRef.Name == "" {
			return &controller.BackupConfigError{Reason: "StorageReferenceMissing", Message: fmt.Sprintf("backup storage %q must set storageRef.name", storage.Name)}
		}

		bs, err := c.BackupStorage(storage.StorageRef.Name)
		if err != nil {
			return &controller.BackupConfigError{Reason: "StorageNotFound", Message: err.Error()}
		}
		if bs.Spec.Type != backupv1alpha1.BackupStorageTypeS3 || bs.Spec.S3 == nil {
			return &controller.BackupConfigError{Reason: "StorageTypeUnsupported", Message: fmt.Sprintf("BackupStorage %q must use type s3", bs.Name)}
		}

		_, _, credErr := c.BackupStorageCredentials(bs)
		if credErr != nil {
			return &controller.BackupConfigError{Reason: "StorageCredentialsError", Message: credErr.Error()}
		}

		opStorage := &pxcv1.BackupStorageSpec{
			Type: pxcv1.BackupStorageS3,
			S3: &pxcv1.BackupStorageS3Spec{
				Bucket:            bs.Spec.S3.Bucket,
				CredentialsSecret: bs.Spec.S3.CredentialsSecretName,
				Region:            bs.Spec.S3.Region,
				EndpointURL:       bs.Spec.S3.EndpointURL,
			},
			VerifyTLS: bs.Spec.S3.VerifyTLS,
		}
		if bs.Spec.S3.ForcePathStyle != nil && *bs.Spec.S3.ForcePathStyle {
			opStorage.ContainerOptions = &pxcv1.BackupContainerOptions{
				Env: []corev1.EnvVar{{
					Name:  "AWS_FORCE_PATH_STYLE",
					Value: "true",
				}},
			}
		}
		backupSpec.Storages[storage.Name] = opStorage

		if storage.PITR != nil && storage.PITR.Enabled {
			pitrEnabled++
			backupSpec.PITR.Enabled = true
			backupSpec.PITR.StorageName = storage.Name

			cfg := &pxcPITRConfig{}
			if storage.PITR.Config != nil && len(storage.PITR.Config.Raw) > 0 {
				if err := json.Unmarshal(storage.PITR.Config.Raw, cfg); err != nil {
					return &controller.BackupConfigError{Reason: "InvalidPITRConfig", Message: fmt.Sprintf("decode PITR config for storage %q: %v", storage.Name, err)}
				}
			}
			if cfg.TimeBetweenUploads != nil {
				backupSpec.PITR.TimeBetweenUploads = *cfg.TimeBetweenUploads
			}
			if cfg.TimeoutSeconds != nil {
				backupSpec.PITR.TimeoutSeconds = *cfg.TimeoutSeconds
			}
		}

		for _, schedule := range storage.Schedules {
			if !schedule.Enabled {
				continue
			}
			s := pxcv1.PXCScheduledBackupSchedule{
				Name:        schedule.Name,
				Schedule:    schedule.Cron,
				StorageName: storage.Name,
			}
			if schedule.RetentionCopies > 0 {
				s.Retention = &pxcv1.PXCScheduledBackupRetention{
					Type:              "count",
					Count:             int(schedule.RetentionCopies),
					DeleteFromStorage: true,
				}
			}
			backupSpec.Schedule = append(backupSpec.Schedule, s)
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

// OperatorBackupType implements controller.BackupMirror (optional).
func (p *PXCProvider) OperatorBackupType() client.Object {
	return &pxcv1.PerconaXtraDBClusterBackup{}
}
