// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
)

func raw(s string) *runtime.RawExtension {
	return &runtime.RawExtension{Raw: []byte(s)}
}

// generatedPXCBackupClassPath is the BackupClass manifest `provider-sdk
// generate` derives from definition/backupclasses/pxc, including the
// parametersSchema/restoreParametersSchema the kubebuilder markers on
// pxc.PxcBackupConfig / PxcRestoreConfig produce. Tests load the real
// generated artifact so they exercise exactly what the provider ships.
const generatedPXCBackupClassPath = "../../charts/provider-percona-xtradb-cluster/generated/backupclasses/pxc.yaml"

func loadPXCBackupClass(t *testing.T) *backupv1alpha1.BackupClass {
	t.Helper()
	data, err := os.ReadFile(generatedPXCBackupClassPath)
	require.NoError(t, err)
	bc := &backupv1alpha1.BackupClass{}
	require.NoError(t, yaml.Unmarshal(data, bc))
	return bc
}

func TestValidateBackupParameters(t *testing.T) {
	t.Parallel()
	bc := loadPXCBackupClass(t)

	require.NoError(t, validateBackupParameters(bc, nil))
	require.NoError(t, validateBackupParameters(bc, raw("")))
	require.NoError(t, validateBackupParameters(bc, raw("null")))
	require.NoError(t, validateBackupParameters(bc, raw("{}")))
	err := validateBackupParameters(bc, raw(`{"type":"full"}`))
	require.ErrorIs(t, err, apicommon.ErrSchemaValidationFailure)
	require.ErrorContains(t, err, "Additional property type is not allowed")
	require.ErrorContains(t, validateBackupParameters(bc, raw(`"full"`)), "failed to unmarshal parameters")

	t.Run("no resolved class rejects any payload", func(t *testing.T) {
		t.Parallel()
		require.ErrorIs(t, validateBackupParameters(nil, raw(`{"type":"full"}`)), apicommon.ErrSchemaValidationFailure)
	})
}

func TestValidateRestoreParameters(t *testing.T) {
	t.Parallel()
	bc := loadPXCBackupClass(t)

	require.NoError(t, validateRestoreParameters(bc, nil))
	require.NoError(t, validateRestoreParameters(bc, raw("{}")))
	err := validateRestoreParameters(bc, raw(`{"target":"x"}`))
	require.ErrorIs(t, err, apicommon.ErrSchemaValidationFailure)
	require.ErrorContains(t, err, "Additional property target is not allowed")
}

func TestValidateBackupScheduleNames(t *testing.T) {
	t.Parallel()

	spec := func(storages ...[]corev1alpha1.InstanceBackupSchedule) *corev1alpha1.InstanceBackupSpec {
		s := &corev1alpha1.InstanceBackupSpec{Enabled: true, ClassRef: apicommon.ObjectRef{Name: "pxc"}}
		for _, list := range storages {
			s.Storages = append(s.Storages, corev1alpha1.InstanceBackupStorage{
				StorageRef: apicommon.ObjectRef{Name: "s3"},
				Schedules:  list,
			})
		}
		return s
	}

	t.Run("nil backup spec is valid", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, validateBackupScheduleNames(nil))
	})

	t.Run("distinct names across storages are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(spec(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "weekly", Cron: "0 2 * * 0"}},
		))
		assert.Empty(t, errs)
	})

	t.Run("two schedules on the same storage are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(spec([]corev1alpha1.InstanceBackupSchedule{
			{Name: "weekly", Cron: "0 1 * * 0"},
			{Name: "monthly", Cron: "0 1 1 * *"},
		}))
		assert.Empty(t, errs)
	})

	t.Run("duplicate schedule name across storages is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupScheduleNames(spec(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 2 * * *"}},
		))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[1].schedules[0].name", errs[0].Field)
	})
}

// testContextWithPXCBackupClass builds a Context over instance with the real
// generated "pxc" BackupClass registered on the fake client, so
// c.BackupClassForInstance() resolves it the way the runtime does.
func testContextWithPXCBackupClass(t *testing.T, instance *corev1alpha1.Instance, extra ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	bc := loadPXCBackupClass(t)
	objs := append([]client.Object{instance, bc}, extra...)
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return controller.NewContext(context.Background(), k8sClient, instance, "percona-xtradb-cluster")
}

func TestValidateBackupScheduleParameters(t *testing.T) {
	t.Parallel()

	schedules := func(list ...corev1alpha1.InstanceBackupSchedule) *corev1alpha1.Instance {
		return &corev1alpha1.Instance{
			ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"},
			Spec: corev1alpha1.InstanceSpec{
				Backup: &corev1alpha1.InstanceBackupSpec{
					Enabled:  true,
					ClassRef: apicommon.ObjectRef{Name: "pxc"},
					Storages: []corev1alpha1.InstanceBackupStorage{{
						StorageRef: apicommon.ObjectRef{Name: "s3"},
						Schedules:  list,
					}},
				},
			},
		}
	}

	t.Run("no backup config is valid", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPXCBackupClass(t, &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"}})
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		assert.Empty(t, errs)
	})

	t.Run("no parameters is valid", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPXCBackupClass(t, schedules(corev1alpha1.InstanceBackupSchedule{Name: "nightly", Cron: "0 1 * * *"}))
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		assert.Empty(t, errs)
	})

	t.Run("undeclared field is rejected", func(t *testing.T) {
		t.Parallel()
		c := testContextWithPXCBackupClass(t, schedules(corev1alpha1.InstanceBackupSchedule{
			Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"compress":true}`),
		}))
		errs, err := validateBackupScheduleParameters(c)
		require.NoError(t, err)
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})
}

func TestSyncRestoreRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"},
		Spec:       corev1alpha1.InstanceSpec{Backup: &corev1alpha1.InstanceBackupSpec{ClassRef: apicommon.ObjectRef{Name: "pxc"}}},
	}
	restore := &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "everest"},
		Spec: backupv1alpha1.RestoreSpec{
			InstanceRef: apicommon.ObjectRef{Name: "pxc-prod"},
			DataSource: backupv1alpha1.DataSource{
				Type:   backupv1alpha1.DataSourceTypeBackup,
				Backup: &backupv1alpha1.DataSourceBackup{BackupRef: apicommon.ObjectRef{Name: "b1"}},
			},
			Parameters: raw(`{"target":"x"}`),
		},
	}
	c := testContextWithPXCBackupClass(t, instance, restore.DeepCopy())

	status, err := (&PXCProvider{}).SyncRestore(c, restore)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
	assert.Contains(t, status.Message, "spec.parameters: schema validation failed")
	assert.Contains(t, status.Message, "Additional property target is not allowed")
}

func TestSyncBackupRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	instance := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"},
		Spec:       corev1alpha1.InstanceSpec{Backup: &corev1alpha1.InstanceBackupSpec{ClassRef: apicommon.ObjectRef{Name: "pxc"}}},
	}
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "everest"},
		Spec: backupv1alpha1.BackupSpec{
			Origin:     backupv1alpha1.BackupOrigin{Type: backupv1alpha1.BackupOriginTypeInstance, InstanceRef: &apicommon.ObjectRef{Name: "pxc-prod"}},
			ClassRef:   apicommon.ObjectRef{Name: "pxc"},
			StorageRef: apicommon.ObjectRef{Name: "s3"},
			Parameters: raw(`{"type":"full"}`),
		},
	}
	c := testContextWithPXCBackupClass(t, instance, backup.DeepCopy())

	status, err := (&PXCProvider{}).SyncBackup(c, backup)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.BackupStateFailed, status.State)
	assert.Contains(t, status.Message, "spec.parameters: schema validation failed")
}
