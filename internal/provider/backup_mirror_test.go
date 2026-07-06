package provider

import (
	"context"
	"strings"
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMirrorScheduledBackupByLabels(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core api to scheme: %v", err)
	}

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			Provider: "percona-xtradb-cluster",
			Backup: &corev1alpha1.InstanceBackupSpec{
				ClassRef: corev1alpha1.BackupClassReference{Name: "pxc"},
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
	if err != nil {
		t.Fatalf("Mirror() error = %v", err)
	}
	if mirror == nil {
		t.Fatal("Mirror() returned nil, expected mirrored Backup")
	}
	if mirror.Spec.ScheduleName != "nightly" {
		t.Fatalf("unexpected scheduleName %q", mirror.Spec.ScheduleName)
	}
	if mirror.Spec.StorageName != "bs-msp-1" {
		t.Fatalf("unexpected storageName %q", mirror.Spec.StorageName)
	}
	if len(mirror.OwnerReferences) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(mirror.OwnerReferences))
	}
	owner := mirror.OwnerReferences[0]
	if owner.APIVersion != pxcv1.SchemeGroupVersion.String() || owner.Kind != "PerconaXtraDBClusterBackup" || owner.Name != opBackup.Name || owner.UID != opBackup.UID {
		t.Fatalf("unexpected owner reference: %#v", owner)
	}
}

func TestMirrorSkipsOnDemandBackup(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core api to scheme: %v", err)
	}

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			Provider: "percona-xtradb-cluster",
			Backup: &corev1alpha1.InstanceBackupSpec{
				ClassRef: corev1alpha1.BackupClassReference{Name: "pxc"},
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
	if err != nil {
		t.Fatalf("Mirror() error = %v", err)
	}
	if mirror != nil {
		t.Fatalf("Mirror() returned backup %q for on-demand backup", mirror.Name)
	}
}

func TestMirrorUsesSchedulerNameWhenProvided(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	if err := corev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core api to scheme: %v", err)
	}

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "inst-qaf", Namespace: "my-special-place"},
		Spec: corev1alpha1.InstanceSpec{
			Provider: "percona-xtradb-cluster",
			Backup:   &corev1alpha1.InstanceBackupSpec{ClassRef: corev1alpha1.BackupClassReference{Name: "pxc"}},
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
	if err != nil {
		t.Fatalf("Mirror() error = %v", err)
	}
	if mirror == nil {
		t.Fatal("Mirror() returned nil, expected mirrored Backup")
	}
	if mirror.Spec.ScheduleName != "nightly" {
		t.Fatalf("unexpected scheduleName %q", mirror.Spec.ScheduleName)
	}
}

func TestDecodeAndValidatePITRConfigRejectsNegativeValues(t *testing.T) {
	t.Parallel()

	_, err := decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":-1}`))
	if err == nil {
		t.Fatal("expected error for negative timeBetweenUploads")
	}
	if !strings.Contains(err.Error(), "timeBetweenUploads must be >= 1") {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeoutSeconds":0}`))
	if err == nil {
		t.Fatal("expected error for non-positive timeoutSeconds")
	}
	if !strings.Contains(err.Error(), "timeoutSeconds must be >= 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeAndValidatePITRConfigAcceptsValidValues(t *testing.T) {
	t.Parallel()

	cfg, err := decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":60,"timeoutSeconds":3600}`))
	if err != nil {
		t.Fatalf("decodeAndValidatePITRConfig() error = %v", err)
	}
	if cfg.TimeBetweenUploads == nil || *cfg.TimeBetweenUploads != 60 {
		t.Fatalf("unexpected timeBetweenUploads: %#v", cfg.TimeBetweenUploads)
	}
	if cfg.TimeoutSeconds == nil || *cfg.TimeoutSeconds != 3600 {
		t.Fatalf("unexpected timeoutSeconds: %#v", cfg.TimeoutSeconds)
	}
}

func TestDecodeAndValidatePITRConfigAppliesDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := decodeAndValidatePITRConfig("bs-msp-1", nil)
	if err != nil {
		t.Fatalf("decodeAndValidatePITRConfig() error = %v", err)
	}
	if cfg.TimeBetweenUploads == nil || *cfg.TimeBetweenUploads != 60 {
		t.Fatalf("unexpected default timeBetweenUploads: %#v", cfg.TimeBetweenUploads)
	}
	if cfg.TimeoutSeconds == nil || *cfg.TimeoutSeconds != 3600 {
		t.Fatalf("unexpected default timeoutSeconds: %#v", cfg.TimeoutSeconds)
	}

	cfg, err = decodeAndValidatePITRConfig("bs-msp-1", []byte(`{"timeBetweenUploads":120}`))
	if err != nil {
		t.Fatalf("decodeAndValidatePITRConfig() error = %v", err)
	}
	if cfg.TimeBetweenUploads == nil || *cfg.TimeBetweenUploads != 120 {
		t.Fatalf("unexpected overridden timeBetweenUploads: %#v", cfg.TimeBetweenUploads)
	}
	if cfg.TimeoutSeconds == nil || *cfg.TimeoutSeconds != 3600 {
		t.Fatalf("unexpected default timeoutSeconds for partial config: %#v", cfg.TimeoutSeconds)
	}
}
