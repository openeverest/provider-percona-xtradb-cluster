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
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

const (
	snapTestInstance  = "branch"
	snapTestNamespace = "team-a"
	snapTestParams    = `{"bootstrap":{"volumeSnapshot":{"name":"snap"}}}`
)

func snapInstance(params string, phase corev1alpha1.InstancePhase) *corev1alpha1.Instance {
	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: snapTestInstance, Namespace: snapTestNamespace, UID: "instance-uid"},
		Spec: corev1alpha1.InstanceSpec{
			Components: map[string]corev1alpha1.ComponentSpec{common.ComponentEngine: {}},
		},
	}
	if params != "" {
		in.Spec.Components[common.ComponentEngine] = corev1alpha1.ComponentSpec{
			Parameters: &runtime.RawExtension{Raw: []byte(params)},
		}
	}
	in.Status.Phase = phase
	return in
}

func snapContext(t *testing.T, in *corev1alpha1.Instance, objs ...client.Object) *controller.Context {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, in)...).Build()
	return controller.NewContext(context.Background(), cl, in, "percona-xtradb-cluster")
}

func volumeSnapshot(name, pvc, instance string) *unstructured.Unstructured {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	snap.SetName(name)
	snap.SetNamespace(snapTestNamespace)
	if instance != "" {
		snap.SetAnnotations(map[string]string{BootstrapInstanceAnnotation: instance})
	}
	snap.Object["spec"] = map[string]any{"source": map[string]any{"persistentVolumeClaimName": pvc}}
	return snap
}

func sourceObjects() []client.Object {
	return []client.Object{
		volumeSnapshot("snap", "datadir-src-pxc-2", snapTestInstance),
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
			Name: "datadir-src-pxc-2", Namespace: snapTestNamespace,
			Labels: map[string]string{instanceLabelKey: "src"},
		}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "internal-src", Namespace: snapTestNamespace},
			Data:       map[string][]byte{"root": []byte("src-root")},
		},
	}
}

func clusterFrom(snapshot string) *pxcv1.PerconaXtraDBCluster {
	pxc := &pxcv1.PerconaXtraDBCluster{ObjectMeta: metav1.ObjectMeta{Name: snapTestInstance, Namespace: snapTestNamespace}}
	pxc.Spec.PXC = &pxcv1.PXCSpec{PodSpec: &pxcv1.PodSpec{VolumeSpec: &pxcv1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{},
	}}}
	if snapshot != "" {
		pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim.DataSource = &corev1.TypedLocalObjectReference{
			Kind: volumeSnapshotGVK.Kind, Name: snapshot,
		}
	}
	return pxc
}

func TestValidateVolumeSnapshotBootstrap(t *testing.T) {
	t.Parallel()
	const ready = corev1alpha1.InstancePhaseReady
	tests := []struct {
		name string
		old  *corev1alpha1.Instance
		in   *corev1alpha1.Instance
		want []string
	}{
		{name: "from a snapshot", in: snapInstance(snapTestParams, "")},
		{
			name: "invalid name",
			in:   snapInstance(`{"bootstrap":{"volumeSnapshot":{"name":"Not_A_Name"}}}`, ""),
			want: []string{"spec.components[engine].parameters.bootstrap.volumeSnapshot.name"},
		},
		{name: "kept", old: snapInstance(snapTestParams, ready), in: snapInstance(snapTestParams, ready)},
		{
			name: "changed",
			old:  snapInstance(snapTestParams, ready),
			in:   snapInstance(`{"bootstrap":{"volumeSnapshot":{"name":"other"}}}`, ready),
			want: []string{"spec.components[engine].parameters.bootstrap.volumeSnapshot"},
		},
		{
			name: "added after creation",
			old:  snapInstance("", ready),
			in:   snapInstance(snapTestParams, ready),
			want: []string{"spec.components[engine].parameters.bootstrap.volumeSnapshot"},
		},
		{
			name: "changed before provisioning",
			old:  snapInstance(snapTestParams, corev1alpha1.InstancePhasePending),
			in:   snapInstance(`{"bootstrap":{"volumeSnapshot":{"name":"other"}}}`, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, err := range validateVolumeSnapshotBootstrap(tt.in, tt.old) {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("with a dataSource", func(t *testing.T) {
		t.Parallel()
		in := snapInstance(snapTestParams, "")
		in.Spec.DataSource = &backupv1alpha1.DataSource{}
		errs := validateVolumeSnapshotBootstrap(in, nil)
		require.Len(t, errs, 1)
		assert.Contains(t, errs[0].Detail, "spec.dataSource")
	})
}

func TestValidateVolumeSnapshotReference(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		objs []client.Object
		want string
	}{
		{name: "authorized", objs: sourceObjects()},
		{name: "missing", want: `VolumeSnapshot "snap" not found`},
		{
			name: "for another instance",
			objs: []client.Object{volumeSnapshot("snap", "datadir-src-pxc-2", "other")},
			want: "isn't authorized",
		},
		{
			name: "not annotated",
			objs: []client.Object{volumeSnapshot("snap", "datadir-src-pxc-2", "")},
			want: "isn't authorized",
		},
		{name: "cluster started from it", objs: []client.Object{clusterFrom("snap")}},
		{name: "cluster started from another", objs: []client.Object{clusterFrom("old")}, want: "is immutable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := snapContext(t, snapInstance(snapTestParams, ""), tt.objs...)
			errs, err := validateVolumeSnapshotReference(c)
			require.NoError(t, err)
			if tt.want == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), tt.want)
		})
	}

	t.Run("cluster without a snapshot gains one", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance(snapTestParams, ""), clusterFrom(""))
		errs, err := validateVolumeSnapshotReference(c)
		require.NoError(t, err)
		require.Len(t, errs, 1)
	})
}

func TestApplyVolumeSnapshotBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("new cluster copies the source's passwords", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance(snapTestParams, ""), sourceObjects()...)
		pxc := clusterFrom("")
		require.NoError(t, applyVolumeSnapshotBootstrap(c, pxc))

		ds := pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim.DataSource
		require.NotNil(t, ds)
		assert.Equal(t, "snapshot.storage.k8s.io", *ds.APIGroup)
		assert.Equal(t, "VolumeSnapshot", ds.Kind)
		assert.Equal(t, "snap", ds.Name)

		secret := &corev1.Secret{}
		require.NoError(t, c.Get(secret, "internal-"+snapTestInstance))
		assert.Equal(t, []byte("src-root"), secret.Data["root"])
		owner := metav1.GetControllerOf(secret)
		require.NotNil(t, owner)
		assert.Equal(t, snapTestInstance, owner.Name)
	})

	t.Run("keeps a pre-created internal secret", func(t *testing.T) {
		t.Parallel()
		objs := append(sourceObjects(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "internal-" + snapTestInstance, Namespace: snapTestNamespace},
			Data:       map[string][]byte{"root": []byte("as-of-snapshot")},
		})
		c := snapContext(t, snapInstance(snapTestParams, ""), objs...)
		require.NoError(t, applyVolumeSnapshotBootstrap(c, clusterFrom("")))

		secret := &corev1.Secret{}
		require.NoError(t, c.Get(secret, "internal-"+snapTestInstance))
		assert.Equal(t, []byte("as-of-snapshot"), secret.Data["root"])
	})

	t.Run("existing cluster keeps the snapshot without reading it", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance(snapTestParams, corev1alpha1.InstancePhaseReady), clusterFrom("snap"))
		pxc := clusterFrom("")
		require.NoError(t, applyVolumeSnapshotBootstrap(c, pxc))
		assert.Equal(t, "snap", startedFrom(pxc))

		exists, err := c.Exists(&corev1.Secret{}, "internal-"+snapTestInstance)
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("unauthorized snapshot", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance(snapTestParams, ""), volumeSnapshot("snap", "datadir-src-pxc-2", "other"))
		assert.Error(t, applyVolumeSnapshotBootstrap(c, clusterFrom("")))
	})

	t.Run("source passwords gone", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance(snapTestParams, ""), sourceObjects()[:2]...)
		err := applyVolumeSnapshotBootstrap(c, clusterFrom(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "internal-src")
	})

	t.Run("no bootstrap", func(t *testing.T) {
		t.Parallel()
		c := snapContext(t, snapInstance("", ""))
		pxc := clusterFrom("")
		require.NoError(t, applyVolumeSnapshotBootstrap(c, pxc))
		assert.Empty(t, startedFrom(pxc))
	})
}
