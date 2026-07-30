package provider

import (
	"context"
	"fmt"
	"time"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	common "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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

	if backup.Labels == nil {
		backup.Labels = map[string]string{}
	}
	if backup.Labels[instanceNameLabelKey] != backup.Spec.InstanceRef.Name {
		origBackupCR := backup.DeepCopy()
		backup.Labels[instanceNameLabelKey] = backup.Spec.InstanceRef.Name
		if err := c.Client().Patch(c.Context(), backup, client.MergeFrom(origBackupCR)); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("patch Backup %q labels: %w", backup.Name, err)
		}
	}

	opRef := &common.TypedObjectRef{
		Group: pxcv1.SchemeGroupVersion.Group,
		Kind:  "PerconaXtraDBClusterBackup",
		Name:  backup.Name,
	}
	managedByRuntime := backup.Spec.ScheduleName == ""
	ensureBackupControllerReference := func(opBackup *pxcv1.PerconaXtraDBClusterBackup) error {
		if err := controllerutil.SetControllerReference(backup, opBackup, c.Client().Scheme()); err != nil {
			return fmt.Errorf("set backup controller reference: %w", err)
		}
		return nil
	}

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
	err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Name}, opBackup)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterBackup %q: %w", backup.Name, err)
		}

		if !managedByRuntime {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStatePending,
				Message:           "Waiting for operator scheduled backup",
				OperatorBackupRef: opRef,
			}, nil
		}

		pxc := &pxcv1.PerconaXtraDBCluster{}
		if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: backup.Namespace, Name: backup.Spec.InstanceRef.Name}, pxc); err != nil {
			if apierrors.IsNotFound(err) {
				return controller.BackupExecutionStatus{
					State:             backupv1alpha1.BackupStatePending,
					Message:           "Waiting for PerconaXtraDBCluster",
					OperatorBackupRef: opRef,
				}, nil
			}
			return controller.BackupExecutionStatus{}, fmt.Errorf("get PerconaXtraDBCluster %q: %w", backup.Spec.InstanceRef.Name, err)
		}

		if pxc.Spec.Backup == nil || pxc.Spec.Backup.Storages == nil {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStateFailed,
				Message:           "No backup storages configured on the cluster",
				OperatorBackupRef: opRef,
			}, nil
		}
		if _, ok := pxc.Spec.Backup.Storages[backup.Spec.StorageRef.Name]; !ok {
			return controller.BackupExecutionStatus{
				State:             backupv1alpha1.BackupStateFailed,
				Message:           fmt.Sprintf("Storage %q is not configured on the cluster", backup.Spec.StorageRef.Name),
				OperatorBackupRef: opRef,
			}, nil
		}

		opBackup = &pxcv1.PerconaXtraDBClusterBackup{
			ObjectMeta: metav1.ObjectMeta{
				Name:      backup.Name,
				Namespace: backup.Namespace,
			},
			Spec: pxcv1.PXCBackupSpec{
				PXCCluster:  backup.Spec.InstanceRef.Name,
				StorageName: backup.Spec.StorageRef.Name,
			},
		}
		if !c.ShouldRetainBackupData(backup) {
			opBackup.Finalizers = []string{pxcBackupDeleteDataFinalizer}
		}
		if err := ensureBackupControllerReference(opBackup); err != nil {
			return controller.BackupExecutionStatus{}, err
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

	if managedByRuntime {
		origBackup := opBackup.DeepCopy()
		if immutableChangeMsg := immutableBackupSpecChangeMessage(opBackup, backup); immutableChangeMsg != "" {
			immutableErr := fmt.Errorf("cannot change immutable backup spec")
			l.Error(
				immutableErr,
				"failed to reconcile backup CR",
				"backup", backup.Name,
				"requestedInstanceName", backup.Spec.InstanceRef.Name,
				"existingInstanceName", opBackup.Spec.PXCCluster,
				"requestedStorageName", backup.Spec.StorageRef.Name,
				"existingStorageName", opBackup.Spec.StorageName,
				"reason", immutableChangeMsg,
			)
		}
		if c.ShouldRetainBackupData(backup) {
			controllerutil.RemoveFinalizer(opBackup, pxcBackupDeleteDataFinalizer)
		} else {
			controllerutil.AddFinalizer(opBackup, pxcBackupDeleteDataFinalizer)
		}
		if err := ensureBackupControllerReference(opBackup); err != nil {
			return controller.BackupExecutionStatus{}, err
		}
		if err := c.Client().Patch(c.Context(), opBackup, client.MergeFrom(origBackup)); err != nil {
			return controller.BackupExecutionStatus{}, fmt.Errorf("patch PerconaXtraDBClusterBackup %q: %w", backup.Name, err)
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

	if restore.Labels == nil {
		restore.Labels = map[string]string{}
	}
	if restore.Labels[instanceNameLabelKey] != restore.Spec.InstanceRef.Name {
		origRestoreCR := restore.DeepCopy()
		restore.Labels[instanceNameLabelKey] = restore.Spec.InstanceRef.Name
		if err := c.Client().Patch(c.Context(), restore, client.MergeFrom(origRestoreCR)); err != nil {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("patch Restore %q labels: %w", restore.Name, err)
		}
	}

	opRef := &common.TypedObjectRef{
		Group: pxcv1.SchemeGroupVersion.Group,
		Kind:  "PerconaXtraDBClusterRestore",
		Name:  restore.Name,
	}

	opBackupName, desiredPITR, pending, err := resolveRestoreSource(c, restore, opRef)
	if err != nil {
		return controller.RestoreExecutionStatus{}, err
	}
	if pending != nil {
		return *pending, nil
	}

	opRestore := &pxcv1.PerconaXtraDBClusterRestore{}
	err = c.Client().Get(c.Context(), client.ObjectKey{Namespace: restore.Namespace, Name: restore.Name}, opRestore)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return controller.RestoreExecutionStatus{}, fmt.Errorf("get PerconaXtraDBClusterRestore %q: %w", restore.Name, err)
		}

		opRestore = &pxcv1.PerconaXtraDBClusterRestore{
			ObjectMeta: metav1.ObjectMeta{Name: restore.Name, Namespace: restore.Namespace},
			Spec: pxcv1.PerconaXtraDBClusterRestoreSpec{
				PXCCluster: restore.Spec.InstanceRef.Name,
				BackupName: opBackupName,
				PITR:       desiredPITR,
			},
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

	origRestore := opRestore.DeepCopy()
	if err := controllerutil.SetControllerReference(restore, opRestore, c.Client().Scheme()); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("set restore controller reference: %w", err)
	}
	if err := c.Client().Patch(c.Context(), opRestore, client.MergeFrom(origRestore)); err != nil {
		return controller.RestoreExecutionStatus{}, fmt.Errorf("patch PerconaXtraDBClusterRestore %q: %w", restore.Name, err)
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

// resolveRestoreSource translates the Restore's data source into the operator
// inputs: the name of the PerconaXtraDBClusterBackup to restore from, and the
// PITR block when rolling a binlog stream forward.
//
// A non-nil RestoreExecutionStatus means the source is not usable yet (or at
// all) and the caller should surface that status verbatim.
func resolveRestoreSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
	opRef *common.TypedObjectRef,
) (string, *pxcv1.PITR, *controller.RestoreExecutionStatus, error) {
	switch restore.Spec.DataSource.Type {
	case backupv1alpha1.DataSourceTypeBackup:
		name, pending, err := resolveBackupSource(c, restore, opRef)
		return name, nil, pending, err
	case backupv1alpha1.DataSourceTypePointInTime:
		return resolvePointInTimeSource(c, restore, opRef)
	default:
		return "", nil, &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            fmt.Sprintf("Unsupported dataSource type %q", restore.Spec.DataSource.Type),
			OperatorRestoreRef: opRef,
		}, nil
	}
}

// resolveBackupSource restores the state captured by a named Backup CR. The
// Backup mirrors an operator backup of the same name.
func resolveBackupSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
	opRef *common.TypedObjectRef,
) (string, *controller.RestoreExecutionStatus, error) {
	ref := restore.Spec.DataSource.Backup
	if ref == nil || ref.BackupRef.Name == "" {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Restore dataSource.backup.backupRef.name is required",
			OperatorRestoreRef: opRef,
		}, nil
	}

	sourceBackup := &backupv1alpha1.Backup{}
	if err := c.Client().Get(c.Context(),
		client.ObjectKey{Namespace: restore.Namespace, Name: ref.BackupRef.Name}, sourceBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return "", &controller.RestoreExecutionStatus{
				State:              backupv1alpha1.RestoreStatePending,
				Message:            "Waiting for source Backup",
				OperatorRestoreRef: opRef,
			}, nil
		}
		return "", nil, fmt.Errorf("get source Backup %q: %w", ref.BackupRef.Name, err)
	}

	if sourceBackup.Status.State == backupv1alpha1.BackupStateFailed {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Source Backup failed; cannot restore",
			OperatorRestoreRef: opRef,
		}, nil
	}

	return sourceBackup.Name, nil, nil
}

// resolvePointInTimeSource rolls a binlog stream forward to a recovery target.
//
// The client names the stream (source Instance + storage) and the target, never
// a backup: PXC needs a base backup to restore before replaying binlogs, and
// selecting it is engine knowledge. The base is the newest Succeeded backup on
// the stream that completed at or before the target.
func resolvePointInTimeSource(
	c *controller.Context,
	restore *backupv1alpha1.Restore,
	opRef *common.TypedObjectRef,
) (string, *pxcv1.PITR, *controller.RestoreExecutionStatus, error) {
	pitr := restore.Spec.DataSource.PointInTime
	if pitr == nil {
		return "", nil, &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Restore dataSource.pointInTime is required when type is \"PointInTime\"",
			OperatorRestoreRef: opRef,
		}, nil
	}
	// A schema rule already enforces this; repeated for paths that bypass
	// admission.
	if pitr.RecoveryTarget == backupv1alpha1.RecoveryTargetDate && pitr.Date == nil {
		return "", nil, &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            "Restore dataSource.pointInTime.date is required when recoveryTarget is \"date\"",
			OperatorRestoreRef: opRef,
		}, nil
	}

	sourceInstance := restore.Spec.InstanceRef.Name
	if pitr.Source.InstanceRef != nil {
		sourceInstance = pitr.Source.InstanceRef.Name
	}
	storageName := pitr.Source.StorageRef.Name

	base, pending := selectPITRBaseBackup(c, restore.Namespace, sourceInstance, storageName, pitr, opRef)
	if pending != nil {
		return "", nil, pending, nil
	}

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{}
	if err := c.Client().Get(c.Context(),
		client.ObjectKey{Namespace: restore.Namespace, Name: base}, opBackup); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, &controller.RestoreExecutionStatus{
				State:              backupv1alpha1.RestoreStatePending,
				Message:            "Waiting for operator backup",
				OperatorRestoreRef: opRef,
			}, nil
		}
		return "", nil, nil, fmt.Errorf("get operator backup %q: %w", base, err)
	}

	if opBackup.Status.State == pxcv1.BackupFailed {
		message := "Operator backup failed; cannot run PITR"
		if opBackup.Status.Error != "" {
			message = opBackup.Status.Error
		}
		return "", nil, &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStateFailed,
			Message:            message,
			OperatorRestoreRef: opRef,
		}, nil
	}
	if opBackup.Status.State != pxcv1.BackupSucceeded {
		return "", nil, &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStatePending,
			Message:            "Waiting for operator backup to complete",
			OperatorRestoreRef: opRef,
		}, nil
	}

	out := &pxcv1.PITR{
		// The binlog stream lives alongside the base backup: both are keyed by
		// the storage the client named, so the backup's resolved status carries
		// the right storage configuration.
		BackupSource: &opBackup.Status,
		Type:         string(pitr.RecoveryTarget),
	}
	if pitr.Date != nil {
		// PXC parses this as node-local time, so normalise to UTC first.
		out.Date = pitr.Date.UTC().Format("2006-01-02 15:04:05")
	}

	return base, out, nil, nil
}

// selectPITRBaseBackup returns the name of the newest Succeeded Backup on the
// given stream that completed at or before the recovery target. Binlogs are
// replayed forward from it, so a base completed *after* the target cannot be
// used.
func selectPITRBaseBackup(
	c *controller.Context,
	namespace, instanceName, storageName string,
	pitr *backupv1alpha1.DataSourcePointInTime,
	opRef *common.TypedObjectRef,
) (string, *controller.RestoreExecutionStatus) {
	list := &backupv1alpha1.BackupList{}
	if err := c.Client().List(c.Context(), list,
		client.InNamespace(namespace),
		client.MatchingFields{controller.IndexBackupInstanceName: instanceName},
	); err != nil {
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStatePending,
			Message:            fmt.Sprintf("Listing backups for instance %q: %v", instanceName, err),
			OperatorRestoreRef: opRef,
		}
	}

	var best *backupv1alpha1.Backup
	for i := range list.Items {
		b := &list.Items[i]
		if b.Spec.StorageRef.Name != storageName ||
			b.Status.State != backupv1alpha1.BackupStateSucceeded ||
			b.Status.CompletedAt == nil {
			continue
		}
		if pitr.Date != nil && b.Status.CompletedAt.After(pitr.Date.Time) {
			continue
		}
		if best == nil || b.Status.CompletedAt.After(best.Status.CompletedAt.Time) {
			best = b
		}
	}

	if best == nil {
		msg := fmt.Sprintf("No Succeeded backup of instance %q on storage %q to recover from", instanceName, storageName)
		if pitr.Date != nil {
			msg = fmt.Sprintf("%s at or before %s", msg, pitr.Date.UTC().Format(time.RFC3339))
		}
		return "", &controller.RestoreExecutionStatus{
			State:              backupv1alpha1.RestoreStatePending,
			Message:            msg,
			OperatorRestoreRef: opRef,
		}
	}

	return best.Name, nil
}

// CleanupBackup deletes the operator backup resource. For DeletionPolicy: Retain,
// remove storage-protection finalizers before deletion to preserve backup data.
// Return true only when fully deleted, false to requeue.
func (p *PXCProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	l := log.FromContext(c.Context())
	l.Info("Cleaning up backup", "name", backup.Name)

	name := backup.Name

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
	return []controller.WatchConfig{
		controller.WatchExternal(
			&pxcv1.PerconaXtraDBClusterBackup{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}),
			controller.ResourceVersionChangedPredicate,
		),
	}
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
		if r.Spec.InstanceRef.Name != instanceName || !r.DeletionTimestamp.IsZero() {
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
	return []controller.WatchConfig{
		controller.WatchExternal(
			&pxcv1.PerconaXtraDBClusterRestore{},
			handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
				return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(obj)}}
			}),
			controller.ResourceVersionChangedPredicate,
		),
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

func immutableBackupSpecChangeMessage(opBackup *pxcv1.PerconaXtraDBClusterBackup, backup *backupv1alpha1.Backup) string {
	if backup.Spec.InstanceRef.Name != opBackup.Spec.PXCCluster {
		return fmt.Sprintf(
			"cannot change backup spec.instanceRef.name after creation (requested %q, existing %q)",
			backup.Spec.InstanceRef.Name,
			opBackup.Spec.PXCCluster,
		)
	}
	if backup.Spec.StorageRef.Name != opBackup.Spec.StorageName {
		return fmt.Sprintf(
			"cannot change backup spec.storageRef.name after creation (requested %q, existing %q)",
			backup.Spec.StorageRef.Name,
			opBackup.Spec.StorageName,
		)
	}

	return ""
}
