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

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	bsTestInstance    = "db"
	bsTestNamespace   = "team-a"
	bsTestCredentials = "s3-credentials"
	bsTestCA          = "s3-ca"
	bsTestS3          = `{"region":"us-east-1","endpointURL":"https://s3.us-east-1.amazonaws.com"}`
	bsTestPath        = "acme-backups/pxc/backups/team-a/db/instance-uid/"
)

var bsTestProviderSpec = corev1alpha1.ProviderSpec{
	DefaultVersion: "8.4.7",
	Versions:       []corev1alpha1.VersionBundle{{Name: "8.4.7"}},
	ComponentTypes: map[string]corev1alpha1.ComponentType{
		common.ComponentBackup: {
			Versions: []corev1alpha1.ComponentVersion{{Version: "8.4.0", Image: "percona/percona-xtrabackup:8.4.0"}},
		},
	},
}

// backupContext returns the context of an Instance backing up to entries,
// with a client whose calls go through funcs; objs are what the cluster
// holds besides the Instance, its Provider and the pxc BackupClass.
func backupContext(t *testing.T, entries []corev1alpha1.InstanceBackupStorage, funcs interceptor.Funcs, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: bsTestInstance, Namespace: bsTestNamespace, UID: "instance-uid"},
		Spec: corev1alpha1.InstanceSpec{
			Backup: &corev1alpha1.InstanceBackupSpec{
				Enabled:  true,
				ClassRef: commonv1alpha1.ObjectRef{Name: "pxc"},
				Storages: entries,
			},
		},
	}
	provider := &corev1alpha1.Provider{
		ObjectMeta: metav1.ObjectMeta{Name: "percona-xtradb-cluster"},
		Spec:       bsTestProviderSpec,
	}
	class := &backupv1alpha1.BackupClass{
		ObjectMeta: metav1.ObjectMeta{Name: "pxc"},
		Spec: backupv1alpha1.BackupClassSpec{
			ExecutionMode: backupv1alpha1.BackupExecutionModeProviderManaged,
			BackupStorageClasses: []backupv1alpha1.BackupStorageClassSupport{
				{Name: "s3", AuthModes: []string{"AccessKey"}},
			},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in, provider, class)...).WithInterceptorFuncs(funcs).Build()
	return controller.NewContext(context.Background(), cl, in, provider.Name)
}

func bsEntry(name string, schedules ...string) corev1alpha1.InstanceBackupStorage {
	entry := corev1alpha1.InstanceBackupStorage{StorageRef: commonv1alpha1.ObjectRef{Name: name}}
	for _, s := range schedules {
		entry.Schedules = append(entry.Schedules, corev1alpha1.InstanceBackupSchedule{Name: s, Enabled: true, Cron: "0 2 * * *"})
	}
	return entry
}

func bsPITREntry(name, parameters string) corev1alpha1.InstanceBackupStorage {
	entry := bsEntry(name)
	entry.PITR = &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}
	if parameters != "" {
		entry.PITR.Parameters = &runtime.RawExtension{Raw: []byte(parameters)}
	}
	return entry
}

// bsStorage is an s3 BackupStorage in acme-backups/pxc, Accepted for its
// generation when accepted is set.
func bsStorage(name string, accepted bool, parameters string, tls *commonv1alpha1.TLSConfig) *backupv1alpha1.BackupStorage {
	bs := &backupv1alpha1.BackupStorage{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: bsTestNamespace, Generation: 1},
		Spec: backupv1alpha1.BackupStorageSpec{
			BackupStorageClassRef: commonv1alpha1.ObjectRef{Name: "s3"},
			Location:              backupv1alpha1.BackupStorageLocation{Bucket: "acme-backups", Prefix: "pxc"},
			Parameters:            &runtime.RawExtension{Raw: []byte(parameters)},
			Authentication: commonv1alpha1.Authentication{
				Mode:                 "AccessKey",
				CredentialsSecretRef: &commonv1alpha1.SecretRef{Name: bsTestCredentials},
			},
			TLS: tls,
		},
	}
	if accepted {
		bs.Status.Conditions = []metav1.Condition{{
			Type:               backupv1alpha1.BackupStorageConditionAccepted,
			Status:             metav1.ConditionTrue,
			Reason:             backupv1alpha1.ReasonAccepted,
			ObservedGeneration: 1,
		}}
	}
	return bs
}

func bsSecrets() []client.Object {
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bsTestCredentials, Namespace: bsTestNamespace},
			Data:       map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("id"), "AWS_SECRET_ACCESS_KEY": []byte("secret")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: bsTestCA, Namespace: bsTestNamespace},
			Data:       map[string][]byte{"ca.crt": []byte("-----BEGIN CERTIFICATE-----")},
		},
	}
}

// bsHeld is the storage block a previous Sync rendered for name.
func bsHeld(name string) *pxcv1.BackupStorageSpec {
	return &pxcv1.BackupStorageSpec{
		Type: pxcv1.BackupStorageS3,
		S3: &pxcv1.BackupStorageS3Spec{
			Bucket:            "old-bucket/" + name,
			CredentialsSecret: "old-credentials",
			Region:            "eu-west-1",
		},
	}
}

func bsLivePXC(pitr pxcv1.PITRSpec, storages ...string) *pxcv1.PerconaXtraDBCluster {
	live := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: bsTestInstance, Namespace: bsTestNamespace},
		Spec: pxcv1.PerconaXtraDBClusterSpec{
			Backup: &pxcv1.BackupSpec{Storages: map[string]*pxcv1.BackupStorageSpec{}, PITR: pitr},
		},
	}
	for _, name := range storages {
		live.Spec.Backup.Storages[name] = bsHeld(name)
	}
	return live
}

func bsReasons(t *testing.T, c *controller.Context) map[string]string {
	t.Helper()
	storages, err := c.BackupStorages()
	require.NoError(t, err)
	reasons := make(map[string]string, len(storages))
	for _, s := range storages {
		reasons[s.Name()] = s.Reason
	}
	return reasons
}

func TestBackupStorageRender(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		parameters string
		tls        *commonv1alpha1.TLSConfig
		want       *pxcv1.BackupStorageSpec
	}{
		{
			name:       "backups are written under the instance's path in the bucket",
			parameters: bsTestS3,
			want: &pxcv1.BackupStorageSpec{
				Type: pxcv1.BackupStorageS3,
				S3: &pxcv1.BackupStorageS3Spec{
					Bucket:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "https://s3.us-east-1.amazonaws.com",
				},
			},
		},
		{
			name:       "a gateway addressed by path gets path-style requests",
			parameters: `{"region":"us-east-1","endpointURL":"http://minio.storage:9000","forcePathStyle":true,"implementation":"minio"}`,
			want: &pxcv1.BackupStorageSpec{
				Type: pxcv1.BackupStorageS3,
				S3: &pxcv1.BackupStorageS3Spec{
					Bucket:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "http://minio.storage:9000",
				},
				ContainerOptions: &pxcv1.BackupContainerOptions{
					Env: []corev1.EnvVar{{Name: "AWS_FORCE_PATH_STYLE", Value: "true"}},
				},
			},
		},
		{
			name:       "gcs is spoken to as plain s3",
			parameters: `{"region":"auto","endpointURL":"https://storage.googleapis.com","implementation":"gcs"}`,
			want: &pxcv1.BackupStorageSpec{
				Type: pxcv1.BackupStorageS3,
				S3: &pxcv1.BackupStorageS3Spec{
					Bucket:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "auto",
					EndpointURL:       "https://storage.googleapis.com",
				},
			},
		},
		{
			name:       "skipping certificate verification turns verifyTLS off",
			parameters: bsTestS3,
			tls:        &commonv1alpha1.TLSConfig{InsecureSkipVerify: true},
			want: &pxcv1.BackupStorageSpec{
				Type: pxcv1.BackupStorageS3,
				S3: &pxcv1.BackupStorageS3Spec{
					Bucket:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "https://s3.us-east-1.amazonaws.com",
				},
				VerifyTLS: new(false),
			},
		},
		{
			name:       "the operator reads a custom CA from the storage's Secret",
			parameters: bsTestS3,
			tls: &commonv1alpha1.TLSConfig{CA: &commonv1alpha1.CABundleSource{
				SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: bsTestCA, Key: "ca.crt"},
			}},
			want: &pxcv1.BackupStorageSpec{
				Type: pxcv1.BackupStorageS3,
				S3: &pxcv1.BackupStorageS3Spec{
					Bucket:            bsTestPath + "main",
					CredentialsSecret: bsTestCredentials,
					Region:            "us-east-1",
					EndpointURL:       "https://s3.us-east-1.amazonaws.com",
					CABundle: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: bsTestCA},
						Key:                  "ca.crt",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := append(bsSecrets(), bsStorage("main", true, tt.parameters, tt.tls))
			c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main", "daily")}, interceptor.Funcs{}, objs...)
			pxc := &pxcv1.PerconaXtraDBCluster{}

			require.NoError(t, applyBackupSettings(c, pxc))

			require.NotNil(t, pxc.Spec.Backup)
			assert.Equal(t, "percona/percona-xtrabackup:8.4.0", pxc.Spec.Backup.Image)
			assert.Equal(t, map[string]*pxcv1.BackupStorageSpec{"main": tt.want}, pxc.Spec.Backup.Storages)
			require.Len(t, pxc.Spec.Backup.Schedule, 1)
			assert.Equal(t, "main", pxc.Spec.Backup.Schedule[0].StorageName)
			assert.Equal(t, map[string]string{"main": ""}, bsReasons(t, c))
		})
	}
}

func TestBackupStorageHoldRule(t *testing.T) {
	t.Parallel()

	caFromMissingSecret := &commonv1alpha1.TLSConfig{CA: &commonv1alpha1.CABundleSource{
		SecretKeyRef: &commonv1alpha1.SecretKeyRef{Name: "gone", Key: "ca.crt"},
	}}

	tests := []struct {
		name          string
		storage       *backupv1alpha1.BackupStorage
		live          *pxcv1.PerconaXtraDBCluster
		wantStorages  map[string]*pxcv1.BackupStorageSpec
		wantSchedules int
		wantReason    string
	}{
		{
			name:          "a storage that is not Accepted keeps the live block",
			storage:       bsStorage("main", false, bsTestS3, nil),
			live:          bsLivePXC(pxcv1.PITRSpec{}, "main"),
			wantStorages:  map[string]*pxcv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		{
			name:         "a storage that is not Accepted is not added",
			storage:      bsStorage("main", false, bsTestS3, nil),
			wantStorages: map[string]*pxcv1.BackupStorageSpec{},
			wantReason:   corev1alpha1.ReasonBackupStorageNotAccepted,
		},
		{
			name:          "a missing storage keeps the live block",
			live:          bsLivePXC(pxcv1.PITRSpec{}, "main"),
			wantStorages:  map[string]*pxcv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonBackupStorageNotFound,
		},
		{
			name:          "a parameter the provider does not know is rejected and held",
			storage:       bsStorage("main", true, `{"region":"us-east-1","endpointURL":"https://s3.us-east-1.amazonaws.com","sse":"aws:kms"}`, nil),
			live:          bsLivePXC(pxcv1.PITRSpec{}, "main"),
			wantStorages:  map[string]*pxcv1.BackupStorageSpec{"main": bsHeld("main")},
			wantSchedules: 1,
			wantReason:    corev1alpha1.ReasonUnsupportedParameter,
		},
		{
			name:         "a CA that cannot be read is rejected and not added",
			storage:      bsStorage("main", true, bsTestS3, caFromMissingSecret),
			wantStorages: map[string]*pxcv1.BackupStorageSpec{},
			wantReason:   corev1alpha1.ReasonBackupStorageNotAccepted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objs := bsSecrets()
			if tt.storage != nil {
				objs = append(objs, tt.storage)
			}
			if tt.live != nil {
				objs = append(objs, tt.live)
			}
			c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main", "daily")}, interceptor.Funcs{}, objs...)
			pxc := &pxcv1.PerconaXtraDBCluster{}

			require.NoError(t, applyBackupSettings(c, pxc))

			require.NotNil(t, pxc.Spec.Backup)
			assert.Equal(t, tt.wantStorages, pxc.Spec.Backup.Storages)
			assert.Len(t, pxc.Spec.Backup.Schedule, tt.wantSchedules, "schedules only target storages the engine has")
			assert.Equal(t, map[string]string{"main": tt.wantReason}, bsReasons(t, c))
		})
	}
}

func TestBackupPITRFollowsRenderedStorages(t *testing.T) {
	t.Parallel()

	livePITR := pxcv1.PITRSpec{Enabled: true, StorageName: "old", TimeBetweenUploads: 60, TimeoutSeconds: 3600}

	tests := []struct {
		name     string
		entries  []corev1alpha1.InstanceBackupStorage
		objs     []client.Object
		wantPITR pxcv1.PITRSpec
	}{
		{
			name:    "PITR on a held storage keeps pointing at it",
			entries: []corev1alpha1.InstanceBackupStorage{bsPITREntry("old", `{"timeBetweenUploads":120}`)},
			objs:    []client.Object{bsStorage("old", false, bsTestS3, nil), bsLivePXC(livePITR, "old")},
			wantPITR: pxcv1.PITRSpec{
				Enabled: true, StorageName: "old", TimeBetweenUploads: 120, TimeoutSeconds: 3600,
			},
		},
		{
			name:     "PITR moved to a storage the engine does not have yet stays on the live one",
			entries:  []corev1alpha1.InstanceBackupStorage{bsEntry("old"), bsPITREntry("new", `{"timeBetweenUploads":120}`)},
			objs:     []client.Object{bsStorage("old", true, bsTestS3, nil), bsStorage("new", false, bsTestS3, nil), bsLivePXC(livePITR, "old")},
			wantPITR: livePITR,
		},
		{
			name:    "PITR on a storage the engine does not have yet is not enabled",
			entries: []corev1alpha1.InstanceBackupStorage{bsPITREntry("new", "")},
			objs:    []client.Object{bsStorage("new", false, bsTestS3, nil)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := backupContext(t, tt.entries, interceptor.Funcs{}, append(bsSecrets(), tt.objs...)...)
			pxc := &pxcv1.PerconaXtraDBCluster{}

			require.NoError(t, applyBackupSettings(c, pxc))

			require.NotNil(t, pxc.Spec.Backup)
			assert.Equal(t, tt.wantPITR, pxc.Spec.Backup.PITR)
			if tt.wantPITR.Enabled {
				assert.Contains(t, pxc.Spec.Backup.Storages, tt.wantPITR.StorageName)
			}
		})
	}
}

func TestBackupStorageAPIErrorsFailSync(t *testing.T) {
	t.Parallel()

	funcs := interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == bsTestCredentials {
				return apierrors.NewServiceUnavailable("etcd is down")
			}
			return cl.Get(ctx, key, obj, opts...)
		},
	}
	objs := append(bsSecrets(), bsStorage("main", true, bsTestS3, nil), bsLivePXC(pxcv1.PITRSpec{}, "main"))
	c := backupContext(t, []corev1alpha1.InstanceBackupStorage{bsEntry("main")}, funcs, objs...)

	err := applyBackupSettings(c, &pxcv1.PerconaXtraDBCluster{})

	require.Error(t, err)
	assert.True(t, apierrors.IsServiceUnavailable(err))
	assert.Equal(t, map[string]string{"main": ""}, bsReasons(t, c), "a transient error is not a verdict on the storage")
}
