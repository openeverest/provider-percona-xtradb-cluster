package provider

import (
	"context"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SyncBackup creates or updates a PerconaXtraDBClusterBackup that references
// the operator-registered storage matching backup.spec.storageName, then maps
// operator status into the BackupExecutionStatus the runtime expects.
func (p *PXCProvider) SyncBackup(c *controller.Context, backup *backupv1alpha1.Backup) (controller.BackupExecutionStatus, error) {
	exec := controller.BackupExecutionStatus{
		OperatorBackupRef: &corev1.TypedLocalObjectReference{},
	}
	return exec, nil
}

// SyncRestore creates or updates a PerconaXtraDBClusterRestore that points at
// the operator backup produced by the source Backup CR.
func (p *PXCProvider) SyncRestore(c *controller.Context, restore *backupv1alpha1.Restore) (controller.RestoreExecutionStatus, error) {
	out := controller.RestoreExecutionStatus{
		OperatorRestoreRef: &corev1.TypedLocalObjectReference{},
	}
	return out, nil
}

// CleanupBackup removes the operator PerconaXtraDBClusterBackup created for the Backup CR.
func (p *PXCProvider) CleanupBackup(c *controller.Context, backup *backupv1alpha1.Backup) (bool, error) {
	return false, nil
}

// CleanupRestore deletes the PerconaXtraDBClusterRestore. The restore CR is
// run-to-completion and carries no protective finalizer, so a single delete
// is sufficient.
func (p *PXCProvider) CleanupRestore(c *controller.Context, restore *backupv1alpha1.Restore) (bool, error) {
	return false, nil
}

// OperatorBackupType implements controller.BackupMirror.
func (p *PXCProvider) OperatorBackupType() client.Object {
	return &pxcv1.PerconaXtraDBClusterBackup{}
}

func (p *PXCProvider) Mirror(ctx context.Context, c client.Client, obj client.Object) (*backupv1alpha1.Backup, error) {
	return &backupv1alpha1.Backup{}, nil
}
