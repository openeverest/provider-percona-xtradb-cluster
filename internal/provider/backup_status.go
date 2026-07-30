package provider

import (
	"fmt"
	"sort"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Reasons published on InstanceBackupStoragePITRStatus.
const (
	pitrReasonWindowAvailable = "WindowAvailable"
	pitrReasonNoBackups       = "NoRestorableBackup"
	pitrReasonBinlogGap       = "BinlogGapDetected"
)

var _ controller.InstanceBackupStatusReporter = (*PXCProvider)(nil)

// BackupStorageStatuses publishes the point-in-time recovery window observed on
// each PITR-enabled storage of the Instance.
func (p *PXCProvider) BackupStorageStatuses(c *controller.Context) ([]corev1alpha1.InstanceBackupStorageStatus, error) {
	in := c.Instance()
	if in.Spec.Backup == nil || len(in.Spec.Backup.Storages) == 0 {
		return nil, nil
	}

	opBackups := &pxcv1.PerconaXtraDBClusterBackupList{}
	if err := c.Client().List(c.Context(), opBackups, client.InNamespace(in.Namespace)); err != nil {
		return nil, fmt.Errorf("list PerconaXtraDBClusterBackups: %w", err)
	}

	out := make([]corev1alpha1.InstanceBackupStorageStatus, 0, len(in.Spec.Backup.Storages))
	for _, storage := range in.Spec.Backup.Storages {
		entry := corev1alpha1.InstanceBackupStorageStatus{Name: storage.StorageRef.Name}
		if storage.PITR != nil && storage.PITR.Enabled {
			entry.PITR = pitrWindow(collectStorageBackups(opBackups.Items, in.Name, storage.StorageRef.Name))
		}
		out = append(out, entry)
	}
	return out, nil
}

// collectStorageBackups returns the Succeeded backups of one cluster on one
// storage, oldest first.
func collectStorageBackups(all []pxcv1.PerconaXtraDBClusterBackup, cluster, storage string) []pxcv1.PerconaXtraDBClusterBackup {
	var out []pxcv1.PerconaXtraDBClusterBackup
	for i := range all {
		b := all[i]
		if b.Spec.PXCCluster != cluster ||
			b.Spec.StorageName != storage ||
			b.Status.State != pxcv1.BackupSucceeded ||
			b.Status.CompletedAt == nil {
			continue
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Status.CompletedAt.Before(out[j].Status.CompletedAt)
	})
	return out
}

// pitrWindow derives the recovery window from the operator's backups.
//
// PXC does not report a window. It reports the end of one --
// status.latestRestorableTime on the newest backup -- and it flags
// discontinuities by setting PITRReady=False on a backup, meaning "the binlog
// stream is broken *after* this backup". The start has to be inferred.
//
// Three properties of the upstream implementation constrain how the flag can be
// read: only the backup that was latest when detection ran is ever marked;
// nothing ever sets the condition back to True, so absence means "not checked"
// rather than "verified healthy"; and detection re-runs against each new latest
// backup, so an unmarked backup *newer* than a marked one is positive evidence
// that the collector stopped reporting a gap.
//
// That yields the only safe rule available from the CRD surface:
//
//	earliest = oldest backup newer than every marked backup
//
// The inverse rule -- oldest backup without the mark -- is unsafe: by the first
// property it returns the oldest backup overall and advertises a window
// spanning the gap.
//
// This discards any restorable segment before the gap. Under-reporting is the
// safe direction: a client reading only the window must never be offered a
// point it cannot restore. Restoring a specific Backup from before the gap is
// unaffected -- only roll-forward is withdrawn.
func pitrWindow(backups []pxcv1.PerconaXtraDBClusterBackup) *corev1alpha1.InstanceBackupStoragePITRStatus {
	if len(backups) == 0 {
		return &corev1alpha1.InstanceBackupStoragePITRStatus{
			State:   corev1alpha1.PITRStateUnavailable,
			Reason:  pitrReasonNoBackups,
			Message: "No Succeeded backup on this storage yet",
		}
	}

	// Index of the newest backup carrying a gap mark, if any.
	lastMarked := -1
	var gapMessage string
	for i := range backups {
		if cond := gapCondition(&backups[i]); cond != nil {
			lastMarked = i
			gapMessage = cond.Message
		}
	}

	if lastMarked == len(backups)-1 {
		msg := "A binlog gap was detected and no backup has completed since"
		if gapMessage != "" {
			msg = fmt.Sprintf("%s: %s", msg, gapMessage)
		}
		return &corev1alpha1.InstanceBackupStoragePITRStatus{
			State:   corev1alpha1.PITRStateUnavailable,
			Reason:  pitrReasonBinlogGap,
			Message: msg,
		}
	}

	clean := backups[lastMarked+1:]
	window := &corev1alpha1.InstanceBackupStoragePITRStatus{
		EarliestRestorableTime: clean[0].Status.CompletedAt,
		LatestRestorableTime:   clean[len(clean)-1].Status.LatestRestorableTime,
		State:                  corev1alpha1.PITRStateAvailable,
		Reason:                 pitrReasonWindowAvailable,
	}
	if lastMarked >= 0 {
		window.Message = "Window truncated at a detected binlog gap; earlier backups are still restorable individually"
	}

	// The operator has not published an end for the stream yet, so nothing is
	// restorable by time even though a base exists.
	if window.LatestRestorableTime == nil {
		return &corev1alpha1.InstanceBackupStoragePITRStatus{
			State:   corev1alpha1.PITRStateUnavailable,
			Reason:  pitrReasonNoBackups,
			Message: "Waiting for the binlog collector to report a restorable time",
		}
	}

	return window
}

// gapCondition returns the PITRReady=False condition on a backup, or nil when
// no gap has been flagged.
func gapCondition(b *pxcv1.PerconaXtraDBClusterBackup) *metav1.Condition {
	for i := range b.Status.Conditions {
		cond := &b.Status.Conditions[i]
		if cond.Type == pxcv1.BackupConditionPITRReady && cond.Status == metav1.ConditionFalse {
			return cond
		}
	}
	return nil
}
