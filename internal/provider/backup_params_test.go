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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
)

func raw(s string) *runtime.RawExtension {
	return &runtime.RawExtension{Raw: []byte(s)}
}

func TestValidateBackupParameters(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateBackupParameters(nil))
	require.NoError(t, validateBackupParameters(raw("")))
	require.NoError(t, validateBackupParameters(raw("null")))
	require.NoError(t, validateBackupParameters(raw("{}")))
	require.ErrorContains(t, validateBackupParameters(raw(`{"type":"full"}`)), `unknown field "type"`)
	require.ErrorContains(t, validateBackupParameters(raw(`"full"`)), "invalid parameters")
	require.ErrorContains(t, validateBackupParameters(raw(`{}{}`)), "unexpected data")
}

func TestValidateRestoreParameters(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateRestoreParameters(nil))
	require.NoError(t, validateRestoreParameters(raw("{}")))
	require.ErrorContains(t, validateRestoreParameters(raw(`{"target":"x"}`)), `unknown field "target"`)
}

func TestValidateBackupSchedules(t *testing.T) {
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
		assert.Empty(t, validateBackupSchedules(nil))
	})

	t.Run("distinct names across storages are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(spec(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "weekly", Cron: "0 2 * * 0"}},
		))
		assert.Empty(t, errs)
	})

	t.Run("two schedules on the same storage are valid", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(spec([]corev1alpha1.InstanceBackupSchedule{
			{Name: "weekly", Cron: "0 1 * * 0"},
			{Name: "monthly", Cron: "0 1 1 * *"},
		}))
		assert.Empty(t, errs)
	})

	t.Run("duplicate schedule name across storages is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(spec(
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 1 * * *"}},
			[]corev1alpha1.InstanceBackupSchedule{{Name: "nightly", Cron: "0 2 * * *"}},
		))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[1].schedules[0].name", errs[0].Field)
	})

	t.Run("schedule parameters with an undeclared field is rejected", func(t *testing.T) {
		t.Parallel()
		errs := validateBackupSchedules(spec([]corev1alpha1.InstanceBackupSchedule{
			{Name: "nightly", Cron: "0 1 * * *", Parameters: raw(`{"compress":true}`)},
		}))
		require.Len(t, errs, 1)
		assert.Equal(t, "spec.backup.storages[0].schedules[0].parameters", errs[0].Field)
	})
}

func TestSyncRestoreRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"}}
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
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, restore.DeepCopy()).Build()
	c := controller.NewContext(context.Background(), k8sClient, instance, "percona-xtradb-cluster")

	status, err := (&PXCProvider{}).SyncRestore(c, restore)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.RestoreStateFailed, status.State)
	assert.Contains(t, status.Message, `spec.parameters: invalid parameters: json: unknown field "target"`)
}

func TestSyncBackupRejectsInvalidParameters(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	instance := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "pxc-prod", Namespace: "everest"}}
	backup := &backupv1alpha1.Backup{
		ObjectMeta: metav1.ObjectMeta{Name: "b1", Namespace: "everest"},
		Spec: backupv1alpha1.BackupSpec{
			Origin:     backupv1alpha1.BackupOrigin{Type: backupv1alpha1.BackupOriginTypeInstance, InstanceRef: &apicommon.ObjectRef{Name: "pxc-prod"}},
			ClassRef:   apicommon.ObjectRef{Name: "pxc"},
			StorageRef: apicommon.ObjectRef{Name: "s3"},
			Parameters: raw(`{"type":"full"}`),
		},
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(instance, backup.DeepCopy()).Build()
	c := controller.NewContext(context.Background(), k8sClient, instance, "percona-xtradb-cluster")

	status, err := (&PXCProvider{}).SyncBackup(c, backup)

	require.NoError(t, err)
	assert.Equal(t, backupv1alpha1.BackupStateFailed, status.State)
	assert.Contains(t, status.Message, `spec.parameters: invalid parameters: json: unknown field "type"`)
}
