package provider

import (
	"testing"
	"time"

	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
)

var pitrBase = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

// mkOpBackup builds a Succeeded operator backup completed at base+offset.
// restorable sets status.latestRestorableTime; gap marks PITRReady=False.
func mkOpBackup(name string, offset time.Duration, restorable bool, gap bool) pxcv1.PerconaXtraDBClusterBackup {
	completed := metav1.NewTime(pitrBase.Add(offset))
	b := pxcv1.PerconaXtraDBClusterBackup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "everest"},
		Spec: pxcv1.PXCBackupSpec{
			PXCCluster:  "pxc-prod",
			StorageName: "minio-primary",
		},
		Status: pxcv1.PXCBackupStatus{
			State:       pxcv1.BackupSucceeded,
			CompletedAt: &completed,
		},
	}
	if restorable {
		t := metav1.NewTime(pitrBase.Add(offset + time.Hour))
		b.Status.LatestRestorableTime = &t
	}
	if gap {
		b.Status.Conditions = append(b.Status.Conditions, metav1.Condition{
			Type:    pxcv1.BackupConditionPITRReady,
			Status:  metav1.ConditionFalse,
			Reason:  "BinlogGapDetected",
			Message: "missing GTID set 1a2b:5-9",
		})
	}
	return b
}

func TestPITRWindow_NoBackups(t *testing.T) {
	t.Parallel()

	got := pitrWindow(nil)
	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateUnavailable, got.State)
	assert.Equal(t, pitrReasonNoBackups, got.Reason)
	assert.Nil(t, got.EarliestRestorableTime)
}

func TestPITRWindow_NoGapSpansAllBackups(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("b1", 0, true, false),
		mkOpBackup("b2", time.Hour, true, false),
		mkOpBackup("b3", 2*time.Hour, true, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateAvailable, got.State)
	// Earliest is the oldest backup; latest comes from the newest.
	assert.Equal(t, pitrBase, got.EarliestRestorableTime.Time)
	assert.Equal(t, pitrBase.Add(3*time.Hour), got.LatestRestorableTime.Time)
	assert.Empty(t, got.Message)
}

func TestPITRWindow_TruncatesAtGap(t *testing.T) {
	t.Parallel()

	// b2 is marked: the stream broke after it. Only b3 onwards can be trusted.
	got := pitrWindow([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("b1", 0, true, false),
		mkOpBackup("b2", time.Hour, true, true),
		mkOpBackup("b3", 2*time.Hour, true, false),
		mkOpBackup("b4", 3*time.Hour, true, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateAvailable, got.State)
	assert.Equal(t, pitrBase.Add(2*time.Hour), got.EarliestRestorableTime.Time,
		"earliest must be the oldest backup newer than the gap, not the oldest overall")
	assert.NotEmpty(t, got.Message, "truncation should be explained")
}

func TestPITRWindow_NewestBackupMarkedIsUnavailable(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("b1", 0, true, false),
		mkOpBackup("b2", time.Hour, true, true),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateUnavailable, got.State)
	assert.Equal(t, pitrReasonBinlogGap, got.Reason)
	assert.Nil(t, got.EarliestRestorableTime)
	assert.Nil(t, got.LatestRestorableTime)
	assert.Contains(t, got.Message, "missing GTID set")
}

// Only the backup that was latest when detection ran is ever marked, so an
// older unmarked backup is not evidence of a healthy stream.
func TestPITRWindow_OlderUnmarkedBackupsDoNotWidenWindow(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("b1", 0, true, false),
		mkOpBackup("b2", time.Hour, true, false),
		mkOpBackup("b3", 2*time.Hour, true, true),
		mkOpBackup("b4", 3*time.Hour, true, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, pitrBase.Add(3*time.Hour), got.EarliestRestorableTime.Time)
}

func TestPITRWindow_NoRestorableTimeYet(t *testing.T) {
	t.Parallel()

	got := pitrWindow([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("b1", 0, false, false),
	})

	require.NotNil(t, got)
	assert.Equal(t, corev1alpha1.PITRStateUnavailable, got.State)
	assert.Nil(t, got.LatestRestorableTime)
}

func TestCollectStorageBackups_FiltersAndSorts(t *testing.T) {
	t.Parallel()

	other := mkOpBackup("other-cluster", 0, true, false)
	other.Spec.PXCCluster = "pxc-staging"

	otherStorage := mkOpBackup("other-storage", 0, true, false)
	otherStorage.Spec.StorageName = "minio-secondary"

	running := mkOpBackup("running", 0, true, false)
	running.Status.State = pxcv1.BackupRunning

	got := collectStorageBackups([]pxcv1.PerconaXtraDBClusterBackup{
		mkOpBackup("newer", time.Hour, true, false),
		other,
		otherStorage,
		running,
		mkOpBackup("older", 0, true, false),
	}, "pxc-prod", "minio-primary")

	require.Len(t, got, 2)
	assert.Equal(t, "older", got[0].Name, "results must be oldest first")
	assert.Equal(t, "newer", got[1].Name)
}
