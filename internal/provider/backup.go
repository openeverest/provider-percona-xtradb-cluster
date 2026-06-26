package provider

import (
	"fmt"
	"reflect"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	pxcBackupDeleteDataFinalizer = "percona.com/delete-backup"
	instanceNameLabelKey         = "instanceName"
)

type pxcPITRConfig struct {
	TimeBetweenUploads *float64 `json:"timeBetweenUploads,omitempty"`
	TimeoutSeconds     *float64 `json:"timeoutSeconds,omitempty"`
}

// Compile-time interface checks.
var _ controller.BackupProvider = (*PXCProvider)(nil)
var _ controller.BackupWatcher = (*PXCProvider)(nil)
var _ controller.RestoreWatcher = (*PXCProvider)(nil)

// SyncBackup creates or updates the operator's backup resource, sets a controller
// reference from the Backup CR to enable owner-based watches, and maps operator
// status to OpenEverest states.
func (p *PXCProvider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	l := log.FromContext(c.Context())
	l.Info("Syncing backup", "name", backup.Name)

	origBackupCR := backup.DeepCopy()
	if backup.Labels == nil {
		backup.Labels = map[string]string{}
	}
	backup.Labels[instanceNameLabelKey] = backup.Spec.InstanceName
	if !reflect.DeepEqual(origBackupCR.Labels, backup.Labels) {
		if err := c.Client().Patch(c.Context(), backup, client.MergeFrom(origBackupCR)); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("patch Backup %q labels: %w", backup.Name, err)
		}
	}

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

// SyncRestore resolves the source Backup CR, creates or updates the operator's
// restore resource with a controller reference, and maps operator status to
// OpenEverest states.
func (p *PXCProvider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	l := log.FromContext(c.Context())
	l.Info("Syncing restore", "name", restore.Name)

	origRestoreCR := restore.DeepCopy()
	if restore.Labels == nil {
		restore.Labels = map[string]string{}
	}
	restore.Labels[instanceNameLabelKey] = restore.Spec.InstanceName
	if !reflect.DeepEqual(origRestoreCR.Labels, restore.Labels) {
		if err := c.Client().Patch(c.Context(), restore, client.MergeFrom(origRestoreCR)); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("patch Restore %q labels: %w", restore.Name, err)
		}
	}

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

// CleanupBackup deletes the operator backup resource. For DeletionPolicy: Retain,
// remove storage-protection finalizers before deletion to preserve backup data.
// Return true only when fully deleted, false to requeue.
func (p *PXCProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up backup", "name", backup.Name)

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

// CleanupRestore deletes the operator restore resource. Return true when fully
// deleted, false to requeue.
func (p *PXCProvider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up restore", "name", restore.Name)

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

// BackupWatches implements controller.BackupWatcher. Register watches so operator
// backup status changes trigger reconciliation. Use WatchOwned for resources with
// controller references set by SyncBackup.
func (p *PXCProvider) BackupWatches() []controller.WatchConfig {
	// TODO: Register watches for your operator backup resource.
	// Example:
	//   return []controller.WatchConfig{
	//       controller.WatchOwned(&operatorv1.MyDatabaseBackup{}),
	//   }
	return []controller.WatchConfig{}
}

// hasActiveRestoreForInstance reports whether the namespace has at least one
// non-terminal Restore for the given instance.
func hasActiveRestoreForInstance(c *controller.Context, namespace, instanceName string) (bool, error) {
	restoreList := &backupv1alpha1.RestoreList{}
	if err := c.Client().List(
		c.Context(),
		restoreList,
		client.InNamespace(namespace),
	); err != nil {
		return false, fmt.Errorf("list Restore resources for instance %q: %w", instanceName, err)
	}

	for i := range restoreList.Items {
		r := restoreList.Items[i]
		if r.Spec.InstanceName != instanceName || !r.DeletionTimestamp.IsZero() {
			continue
		}
		switch r.Status.State {
		case backupv1alpha1.RestoreStateSucceeded, backupv1alpha1.RestoreStateFailed:
			continue
		default:
			return true, nil
		}
	}

	return false, nil
}

// RestoreWatches implements controller.RestoreWatcher. Register watches so operator
// restore status changes trigger reconciliation. Use WatchOwned for resources with
// controller references set by SyncRestore.
func (p *PXCProvider) RestoreWatches() []controller.WatchConfig {
	// TODO: Register watches for your operator restore resource.
	// Example:
	//   return []controller.WatchConfig{
	//       controller.WatchOwned(&operatorv1.MyDatabaseRestore{}),
	//   }
	return []controller.WatchConfig{}
}

func ptrTo[T any](v T) *T {
	return &v
}
