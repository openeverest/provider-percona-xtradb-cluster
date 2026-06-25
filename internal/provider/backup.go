package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	pxcBackupDeleteDataFinalizer = "percona.com/delete-backup"
)

type pxcPITRConfig struct {
	TimeBetweenUploads *float64 `json:"timeBetweenUploads,omitempty"`
	TimeoutSeconds     *float64 `json:"timeoutSeconds,omitempty"`
}

// SyncBackup creates or updates a PerconaXtraDBClusterBackup that references
// the operator-registered storage matching backup.spec.storageName, then maps
// operator status into the BackupExecutionStatus the runtime expects.
func (p *PXCProvider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	opRef := &corev1.TypedLocalObjectReference{
		APIGroup: ptrTo(pxcv1.SchemeGroupVersion.Group),
		Kind:     "PerconaXtraDBClusterBackup",
		Name:     backup.Name,
	}

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Name}, opBackup)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterBackup %q: %w", backup.Name, err)
		}

		pxc := &pxcv1.PerconaXtraDBCluster{}
		if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Spec.InstanceName}, pxc); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.BackupExecutionStatus{
					State:             backupv1alpha1.BackupStatePending,
					Message:           "Waiting for PerconaXtraDBCluster",
					OperatorBackupRef: opRef,
				}, nil
			}
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaXtraDBCluster %q: %w", backup.Spec.InstanceName, err)
		}

		if pxc.Spec.Backup == nil || pxc.Spec.Backup.Storages == nil {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStateFailed,
				Message:           "No backup storages configured on the cluster",
				OperatorBackupRef: opRef,
			}, nil
		}
		if _, ok := pxc.Spec.Backup.Storages[backup.Spec.StorageName]; !ok {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStateFailed,
				Message:           fmt.Sprintf("Storage %q is not configured on the cluster", backup.Spec.StorageName),
				OperatorBackupRef: opRef,
			}, nil
		}

		opBackup = &pxcv1.PerconaXtraDBClusterBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backup.Name,
				Namespace: backup.Namespace,
			},
			Spec: pxcv1.PXCBackupSpec{
				PXCCluster:  backup.Spec.InstanceName,
				StorageName: backup.Spec.StorageName,
			},
		}
		if !c.ShouldRetainBackupData(backup) {
			opBackup.Finalizers = []string{pxcBackupDeleteDataFinalizer}
		}
		if err := controllerutil.SetControllerReference(backup, opBackup, c.Client().Scheme()); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("set backup controller reference: %w", err)
		}
		if err := c.Client().Create(c.Context(), opBackup); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return controller.BackupExecutionStatus{}, fmt.Errorf("create PerconaXtraDBClusterBackup %q: %w", backup.Name, err)
			}
			if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Name}, opBackup); err != nil {
				return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterBackup %q after AlreadyExists: %w", backup.Name, err)
			}
		}
	}

	origBackup := opBackup.DeepCopy()
	if backup.Spec.InstanceName != opBackup.Spec.PXCCluster || backup.Spec.StorageName != opBackup.Spec.StorageName {
		opBackup.Spec.PXCCluster = backup.Spec.InstanceName
		opBackup.Spec.StorageName = backup.Spec.StorageName
	}
	if c.ShouldRetainBackupData(backup) {
		controllerutil.RemoveFinalizer(opBackup, pxcBackupDeleteDataFinalizer)
	} else {
		controllerutil.AddFinalizer(opBackup, pxcBackupDeleteDataFinalizer)
	}
	if err := controllerutil.SetControllerReference(backup, opBackup, c.Client().Scheme()); err != nil {
		return controller.BackupExecutionStatus{}, fmt.Errorf("set backup controller reference: %w", err)
	}
	if !reflect.DeepEqual(origBackup.Spec, opBackup.Spec) ||
		!reflect.DeepEqual(origBackup.Finalizers, opBackup.Finalizers) ||
		!reflect.DeepEqual(origBackup.OwnerReferences, opBackup.OwnerReferences) {
		if err := c.Client().Update(c.Context(), opBackup); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("update PerconaXtraDBClusterBackup %q: %w", backup.Name, err)
		}
	}

	exec := controller.BackupExecutionStatus{
		OperatorBackupRef: opRef,
		Message:           string(opBackup.Status.State),
	}

	if !opBackup.CreationTimestamp.IsZero() {
		t := opBackup.CreationTimestamp
		exec.StartedAt = &t
	}

	switch opBackup.Status.State {
	case pxcv1.BackupFailed:
		exec.State = backupv1alpha1.BackupStateFailed
		if opBackup.Status.Error != "" {
			exec.Message = opBackup.Status.Error
		}
	case pxcv1.BackupSucceeded:
		exec.State = backupv1alpha1.BackupStateSucceeded
		exec.CompletedAt = opBackup.Status.CompletedAt
		exec.Message = "Backup completed"
	case pxcv1.BackupRunning, pxcv1.BackupStarting:
		exec.State = backupv1alpha1.BackupStateRunning
		exec.Message = "Backup is running"
	default:
		exec.State = backupv1alpha1.BackupStatePending
		exec.Message = "Backup is pending"
	}

	return exec, nil
}

// SyncRestore creates or updates a PerconaXtraDBClusterRestore that points at
// the operator backup produced by the source Backup CR.
func (p *PXCProvider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	opRef := &corev1.TypedLocalObjectReference{
		APIGroup: ptrTo(pxcv1.SchemeGroupVersion.Group),
		Kind:     "PerconaXtraDBClusterRestore",
		Name:     restore.Name,
	}

	if restore.Spec.DataSource.Backup == nil || restore.Spec.DataSource.Backup.BackupName == "" {
		return controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Restore dataSource.backup.backupName is required",
			OperatorRestoreRef: opRef,
		}, nil
	}

	sourceBackup := &backupv1alpha1.Backup{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Spec.DataSource.Backup.BackupName}, sourceBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return controller.RestoreExecutionStatus{
				State:              backupv1alpha1.RestoreStatePending,
				Message:            "Waiting for source Backup",
				OperatorRestoreRef: opRef,
			}, nil
		}
		return controller.RestoreExecutionStatus{}, fmt.Errorf("get source Backup %q: %w", restore.Spec.DataSource.Backup.BackupName, err)
	}

	opBackupName := sourceBackup.Name
	if sourceBackup.Status.OperatorBackupRef != nil && sourceBackup.Status.OperatorBackupRef.Name != "" {
		opBackupName = sourceBackup.Status.OperatorBackupRef.Name
	}

	opRestore := &pxcv1.PerconaXtraDBClusterRestore{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, opRestore)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterRestore %q: %w", restore.Name, err)
		}

		opRestore = &pxcv1.PerconaXtraDBClusterRestore{
			ObjectMeta: metav1.ObjectMeta{Name: restore.Name, Namespace: restore.Namespace},
			Spec: pxcv1.PerconaXtraDBClusterRestoreSpec{
				PXCCluster: restore.Spec.InstanceName,
				BackupName: opBackupName,
			},
		}
		if restore.Spec.DataSource.Backup.PITR != nil {
			opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
			if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: opBackupName}, opBackup); err != nil {
				if apierrors.IsNotFound(err) {
					return controller.RestoreExecutionStatus{
						State:              backupv1alpha1.RestoreStatePending,
						Message:            "Waiting for operator backup",
						OperatorRestoreRef: opRef,
					}, nil
				}
				return controller.RestoreExecutionStatus{}, fmt.Errorf("get operator backup %q: %w", opBackupName, err)
			}

			opRestore.Spec.PITR = &pxcv1.PITR{
				BackupSource: &opBackup.Status,
				Type:         string(restore.Spec.DataSource.Backup.PITR.Type),
			}
			if restore.Spec.DataSource.Backup.PITR.Date != nil {
				opRestore.Spec.PITR.Date = restore.Spec.DataSource.Backup.PITR.Date.UTC().Format("2006-01-02 15:04:05")
			}
		}

		if err := controllerutil.SetControllerReference(restore, opRestore, c.Client().Scheme()); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("set restore controller reference: %w", err)
		}
		if err := c.Client().Create(c.Context(), opRestore); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return controller.RestoreExecutionStatus{}, fmt.Errorf("create PerconaXtraDBClusterRestore %q: %w", restore.Name, err)
			}
			if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, opRestore); err != nil {
				return controller.RestoreExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterRestore %q after AlreadyExists: %w", restore.Name, err)
			}
		}
	}

	opRestore.Spec.PXCCluster = restore.Spec.InstanceName
	opRestore.Spec.BackupName = opBackupName
	if restore.Spec.DataSource.Backup.PITR == nil {
		opRestore.Spec.PITR = nil
	} else {
		opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
		if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: opBackupName}, opBackup); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.RestoreExecutionStatus{
					State:              backupv1alpha1.RestoreStatePending,
					Message:            "Waiting for operator backup",
					OperatorRestoreRef: opRef,
				}, nil
			}
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get operator backup %q: %w", opBackupName, err)
		}
		opRestore.Spec.PITR = &pxcv1.PITR{
			BackupSource: &opBackup.Status,
			Type:         string(restore.Spec.DataSource.Backup.PITR.Type),
		}
		if restore.Spec.DataSource.Backup.PITR.Date != nil {
			opRestore.Spec.PITR.Date = restore.Spec.DataSource.Backup.PITR.Date.UTC().Format("2006-01-02 15:04:05")
		}
	}

	origRestore := opRestore.DeepCopy()
	if err := controllerutil.SetControllerReference(restore, opRestore, c.Client().Scheme()); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("set restore controller reference: %w", err)
	}
	if !reflect.DeepEqual(origRestore.Spec, opRestore.Spec) ||
		!reflect.DeepEqual(origRestore.OwnerReferences, opRestore.OwnerReferences) {
		if err := c.Client().Update(c.Context(), opRestore); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("update PerconaXtraDBClusterRestore %q: %w", restore.Name, err)
		}
	}

	out := controller.RestoreExecutionStatus{
		OperatorRestoreRef: opRef,
		Message:            string(opRestore.Status.State),
	}

	if !opRestore.CreationTimestamp.IsZero() {
		t := opRestore.CreationTimestamp
		out.StartedAt = &t
	}

	switch opRestore.Status.State {
	case pxcv1.RestoreFailed:
		out.State = backupv1alpha1.RestoreStateFailed
		if opRestore.Status.Comments != "" {
			out.Message = opRestore.Status.Comments
		}
	case pxcv1.RestoreSucceeded:
		out.State = backupv1alpha1.RestoreStateSucceeded
		out.CompletedAt = opRestore.Status.CompletedAt
		out.Message = "Restore completed"
	case pxcv1.RestoreStarting, pxcv1.RestoreStopCluster, pxcv1.RestoreRestore, pxcv1.RestoreStartCluster, pxcv1.RestorePITR, pxcv1.RestorePrepareCluster:
		out.State = backupv1alpha1.RestoreStateRunning
		out.Message = "Restore is running"
	default:
		out.State = backupv1alpha1.RestoreStatePending
		out.Message = "Restore is pending"
	}

	return out, nil
}

// CleanupBackup removes the operator PerconaXtraDBClusterBackup created for the Backup CR.
func (p *PXCProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	name := backup.Name
	if backup.Status.OperatorBackupRef != nil && backup.Status.OperatorBackupRef.Name != "" {
		name = backup.Status.OperatorBackupRef.Name
	}

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: name}, opBackup)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get PerconaXtraDBClusterBackup %q: %w", name, err)
	}

	if c.ShouldRetainBackupData(backup) {
		if controllerutil.ContainsFinalizer(opBackup, pxcBackupDeleteDataFinalizer) {
			controllerutil.RemoveFinalizer(opBackup, pxcBackupDeleteDataFinalizer)
			if err := c.Client().Update(c.Context(), opBackup); err != nil {
				return false, fmt.Errorf("remove backup finalizer on %q: %w", name, err)
			}
			return false, nil
		}
	}

	if opBackup.DeletionTimestamp.IsZero() {
		if err := c.Client().Delete(c.Context(), opBackup); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete PerconaXtraDBClusterBackup %q: %w", name, err)
		}
	}

	return false, nil
}

// CleanupRestore deletes the PerconaXtraDBClusterRestore. The restore CR is
// run-to-completion and carries no protective finalizer, so a single delete
// is sufficient.
func (p *PXCProvider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	name := restore.Name
	if restore.Status.OperatorRestoreRef != nil && restore.Status.OperatorRestoreRef.Name != "" {
		name = restore.Status.OperatorRestoreRef.Name
	}

	opRestore := &pxcv1.PerconaXtraDBClusterRestore{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: name}, opRestore)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("get PerconaXtraDBClusterRestore %q: %w", name, err)
	}

	if opRestore.DeletionTimestamp.IsZero() {
		if err := c.Client().Delete(c.Context(), opRestore); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete PerconaXtraDBClusterRestore %q: %w", name, err)
		}
	}

	return false, nil
}

// OperatorBackupType implements controller.BackupMirror.
func (p *PXCProvider) OperatorBackupType() client.Object {
	return &pxcv1.PerconaXtraDBClusterBackup{}
}

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

func ptrTo[T any](v T) *T {
	return &v
}
