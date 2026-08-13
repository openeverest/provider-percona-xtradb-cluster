package provider

import (
	"context"
	"testing"

	common "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMirrorScheduledBackupByLabels(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: common.ObjectRef{Name: "percona-xtradb-cluster"},
			Backup: &corev1alpha1.InstanceBackupSpec{
				ClassRef: common.ObjectRef{Name: "pxc"},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	ancestor := scheduledBackupPrefix("my-special-place", "inst-qaf") + "-nightly"
	opBackup := &pxcv1.PerconaXtraDBClusterBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cron-inst-qaf-bs-msp-1-2026630141710-3lll6",
			Namespace: "my-special-place",
			UID:       "11111111-1111-1111-1111-111111111111",
			Labels: map[string]string{
				naming.LabelPerconaBackupType:         backupTypeCron,
				naming.LabelPerconaBackupAncestorName: ancestor,
			},
		},
		Spec: pxcv1.PXCBackupSpec{
			PXCCluster:  "inst-qaf",
			StorageName: "bs-msp-1",
		},
	}

	mirror, err := (&PXCProvider{}).Mirror(context.Background(), k8sClient, opBackup)
	require.NoError(t, err)
	require.NotNil(t, mirror)
	require.Equal(t, "nightly", mirror.Spec.ScheduleName)
	require.Equal(t, "bs-msp-1", mirror.Spec.StorageRef.Name)
	require.Len(t, mirror.OwnerReferences, 1)
	owner := mirror.OwnerReferences[0]
	require.Equal(t, pxcv1.SchemeGroupVersion.String(), owner.APIVersion)
	require.Equal(t, "PerconaXtraDBClusterBackup", owner.Kind)
	require.Equal(t, opBackup.Name, owner.Name)
	require.Equal(t, opBackup.UID, owner.UID)
}

func TestMirrorSkipsOnDemandBackup(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: common.ObjectRef{Name: "percona-xtradb-cluster"},
			Backup: &corev1alpha1.InstanceBackupSpec{
				ClassRef: common.ObjectRef{Name: "pxc"},
			},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "manual-backup", Namespace: "my-special-place"},
		Spec: pxcv1.PXCBackupSpec{
			PXCCluster:  "inst-qaf",
			StorageName: "bs-msp-1",
		},
	}

	mirror, err := (&PXCProvider{}).Mirror(context.Background(), k8sClient, opBackup)
	require.NoError(t, err)
	require.Nil(t, mirror)
}

func TestMirrorUsesSchedulerNameWhenProvided(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			ProviderRef: common.ObjectRef{Name: "percona-xtradb-cluster"},
			Backup:      &corev1alpha1.InstanceBackupSpec{ClassRef: common.ObjectRef{Name: "pxc"}},
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance).Build()

	opBackup := &pxcv1.PerconaXtraDBClusterBackup{
		ObjectMeta: metav1.ObjectMeta{Name: "cron-backup", Namespace: "my-special-place"},
		Spec: pxcv1.PXCBackupSpec{
			PXCCluster:  "inst-qaf",
			StorageName: "bs-msp-1",
		},
		SchedulerName: "nightly",
	}

	mirror, err := (&PXCProvider{}).Mirror(context.Background(), k8sClient, opBackup)
	require.NoError(t, err)
	require.NotNil(t, mirror)
	require.Equal(t, "nightly", mirror.Spec.ScheduleName)
}

func TestDecodeAndValidatePITRConfigRejectsNegativeValues(t *testing.T) {
	t.Parallel()

	_, err := decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":-1}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "timeBetweenUploads must be >= 1")

	_, err = decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeoutSeconds":0}`))
	require.Error(t, err)
	require.ErrorContains(t, err, "timeoutSeconds must be >= 1")
}

func TestDecodeAndValidatePITRConfigAcceptsValidValues(t *testing.T) {
	t.Parallel()

	cfg, err := decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":60,"timeoutSeconds":3600}`))
	require.NoError(t, err)
	expected := &pxcPITRConfig{
		TimeBetweenUploads: ptrTo(60.0),
		TimeoutSeconds:     ptrTo(3600.0),
	}
	require.Equal(t, expected, cfg)
}

func TestDecodeAndValidatePITRConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := decodeAndValidatePITRConfig("bs-msp-1", nil)
	require.NoError(t, err)
	require.Equal(t, &pxcPITRConfig{
		TimeBetweenUploads: ptrTo(60.0),
		TimeoutSeconds:     ptrTo(3600.0),
	}, cfg)

	cfg, err = decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":120}`))
	require.NoError(t, err)
	require.Equal(t, &pxcPITRConfig{
		TimeBetweenUploads: ptrTo(120.0),
		TimeoutSeconds:     ptrTo(3600.0),
	}, cfg)
}
