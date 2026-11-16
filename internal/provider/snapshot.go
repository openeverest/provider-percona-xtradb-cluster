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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/openeverest/provider-percona-xtradb-cluster/definition/components"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

const (
	// BootstrapInstanceAnnotation on a VolumeSnapshot names the one Instance
	// allowed to start from it. Everest users can't annotate snapshots, so
	// only whoever can, such as the Branching plugin, authorizes one.
	BootstrapInstanceAnnotation = "provider-percona-xtradb-cluster.openeverest.io/bootstrap-instance"
	// internalSecretPrefix names the operator's Secret of the system users'
	// current passwords, which the pods start with and the operator logs in
	// with.
	internalSecretPrefix = "internal-"
	// instanceLabelKey is the label the operator puts on a cluster's PVCs.
	instanceLabelKey = "app.kubernetes.io/instance"
)

// VolumeSnapshots and PVCs are read unstructured: the client reads them from
// the API server, with get only, instead of caching them all.
var volumeSnapshotGVK = schema.GroupVersionKind{Group: "snapshot.storage.k8s.io", Version: "v1", Kind: "VolumeSnapshot"}

var bootstrapSnapshotPath = field.NewPath("spec", "components").Key(common.ComponentEngine).
	Child("parameters", "bootstrap", "volumeSnapshot")

// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get

// bootstrapSnapshot is the VolumeSnapshot the Instance asks to start from, or "".
func bootstrapSnapshot(in *corev1alpha1.Instance) (string, error) {
	engine, ok := in.Spec.Components[common.ComponentEngine]
	if !ok || engine.Parameters == nil || len(engine.Parameters.Raw) == 0 {
		return "", nil
	}
	var params components.PXCParameters
	if err := json.Unmarshal(engine.Parameters.Raw, &params); err != nil {
		return "", fmt.Errorf("decode engine parameters: %w", err)
	}
	if params.Bootstrap == nil || params.Bootstrap.VolumeSnapshot == nil {
		return "", nil
	}
	return params.Bootstrap.VolumeSnapshot.Name, nil
}

// provisioned reports whether the Instance got past validation, so its
// cluster may exist.
func provisioned(in *corev1alpha1.Instance) bool {
	return in != nil && in.Status.Phase != "" && in.Status.Phase != corev1alpha1.InstancePhasePending
}

// validateVolumeSnapshotBootstrap checks a bootstrap from a VolumeSnapshot
// against the rest of the Instance, and that it doesn't change once the
// Instance is provisioned: the StatefulSet's volume claim template carries
// it, and that template can't change.
func validateVolumeSnapshotBootstrap(in, old *corev1alpha1.Instance) field.ErrorList {
	name, err := bootstrapSnapshot(in)
	if err != nil {
		return nil // reported by the parameter schema
	}
	var errs field.ErrorList
	if provisioned(old) {
		if oldName, err := bootstrapSnapshot(old); err == nil && oldName != name {
			errs = append(errs, field.Forbidden(bootstrapSnapshotPath, "is immutable"))
		}
	}
	if name == "" {
		return errs
	}
	if msgs := validation.IsDNS1123Subdomain(name); len(msgs) > 0 {
		errs = append(errs, field.Invalid(bootstrapSnapshotPath.Child("name"), name, strings.Join(msgs, "; ")))
	}
	if in.Spec.DataSource != nil {
		errs = append(errs, field.Forbidden(bootstrapSnapshotPath, "cannot be combined with spec.dataSource"))
	}
	return errs
}

// validateVolumeSnapshotReference checks, before the cluster exists, that
// the bootstrap VolumeSnapshot exists and is authorized for this Instance;
// once the cluster exists, that it's the snapshot the cluster started from.
func validateVolumeSnapshotReference(c *controller.Context) (field.ErrorList, error) {
	name, err := bootstrapSnapshot(c.Instance())
	if err != nil {
		return nil, nil
	}
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(pxc, c.Name()); err == nil {
		if startedFrom(pxc) != name {
			return field.ErrorList{field.Forbidden(bootstrapSnapshotPath, "is immutable")}, nil
		}
		return nil, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get PerconaXtraDBCluster: %w", err)
	}
	if name == "" {
		return nil, nil
	}
	if _, err := authorizedSnapshot(c, name); err != nil {
		var invalid *snapshotError
		if errors.As(err, &invalid) {
			return field.ErrorList{field.Invalid(bootstrapSnapshotPath.Child("name"), name, invalid.msg)}, nil
		}
		return nil, err
	}
	return nil, nil
}

// startedFrom is the VolumeSnapshot a cluster's data volumes are made from, or "".
func startedFrom(pxc *pxcv1.PerconaXtraDBCluster) string {
	if pxc.Spec.PXC == nil || pxc.Spec.PXC.VolumeSpec == nil || pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim == nil {
		return ""
	}
	ds := pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim.DataSource
	if ds == nil || ds.Kind != volumeSnapshotGVK.Kind {
		return ""
	}
	return ds.Name
}

// snapshotError is a snapshot that can't be used, as opposed to a failed read.
type snapshotError struct{ msg string }

func (e *snapshotError) Error() string { return e.msg }

// authorizedSnapshot returns the VolumeSnapshot if it exists and is
// annotated for this Instance.
func authorizedSnapshot(c *controller.Context, name string) (*unstructured.Unstructured, error) {
	snap := &unstructured.Unstructured{}
	snap.SetGroupVersionKind(volumeSnapshotGVK)
	if err := c.Get(snap, name); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &snapshotError{fmt.Sprintf("VolumeSnapshot %q not found", name)}
		}
		return nil, fmt.Errorf("get VolumeSnapshot %q: %w", name, err)
	}
	if got := snap.GetAnnotations()[BootstrapInstanceAnnotation]; got != c.Name() {
		return nil, &snapshotError{fmt.Sprintf("VolumeSnapshot %q isn't authorized to start instance %q: it needs the annotation %s=%s",
			name, c.Name(), BootstrapInstanceAnnotation, c.Name())}
	}
	return snap, nil
}

// applyVolumeSnapshotBootstrap makes the cluster's data volumes from the
// Instance's bootstrap VolumeSnapshot. The volume claim template keeps the
// snapshot for the cluster's life: a node added later starts from it too,
// then catches up from the cluster.
//
// Before the cluster exists it also gives the cluster the system users'
// passwords the snapshot's data holds, those of the instance the snapshot
// was taken of: the pods start with, and the operator logs in with, the
// passwords in internal-<cluster>. The operator generates new ones in the
// cluster's users Secret and changes them, so the new instance doesn't keep
// its source's passwords.
func applyVolumeSnapshotBootstrap(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) error {
	name, err := bootstrapSnapshot(c.Instance())
	if err != nil || name == "" {
		return err
	}
	existing := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(existing, c.Name()); apierrors.IsNotFound(err) {
		snap, err := authorizedSnapshot(c, name)
		if err != nil {
			return err
		}
		if err := copyInternalSecret(c, snap); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("get PerconaXtraDBCluster: %w", err)
	}
	if pxc.Spec.PXC.VolumeSpec == nil {
		pxc.Spec.PXC.VolumeSpec = &pxcv1.VolumeSpec{}
	}
	if pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim == nil {
		pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim = &corev1.PersistentVolumeClaimSpec{}
	}
	pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim.DataSource = &corev1.TypedLocalObjectReference{
		APIGroup: ptr.To(volumeSnapshotGVK.Group),
		Kind:     volumeSnapshotGVK.Kind,
		Name:     name,
	}
	return nil
}

// copyInternalSecret creates internal-<cluster> as a copy of the snapshot's
// source cluster's, owned by the Instance. An existing one, such as the
// Branching plugin's copy of the passwords as of the snapshot, is kept.
func copyInternalSecret(c *controller.Context, snap *unstructured.Unstructured) error {
	dstName := internalSecretPrefix + c.Name()
	dst := &corev1.Secret{}
	if err := c.Get(dst, dstName); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get Secret %q: %w", dstName, err)
	}

	pvcName, _, _ := unstructured.NestedString(snap.Object, "spec", "source", "persistentVolumeClaimName")
	pvc := &unstructured.Unstructured{}
	pvc.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("PersistentVolumeClaim"))
	if err := c.Get(pvc, pvcName); err != nil {
		if apierrors.IsNotFound(err) {
			return &snapshotError{fmt.Sprintf("VolumeSnapshot %q: its volume %q is gone, so its cluster's passwords can't be found", snap.GetName(), pvcName)}
		}
		return fmt.Errorf("get PVC %q: %w", pvcName, err)
	}
	source := pvc.GetLabels()[instanceLabelKey]
	if source == "" {
		return &snapshotError{fmt.Sprintf("VolumeSnapshot %q isn't of a PXC volume: PVC %q has no %s label", snap.GetName(), pvcName, instanceLabelKey)}
	}
	src := &corev1.Secret{}
	if err := c.Get(src, internalSecretPrefix+source); err != nil {
		if apierrors.IsNotFound(err) {
			return &snapshotError{fmt.Sprintf("Secret %q of cluster %q, the snapshot's source, not found", internalSecretPrefix+source, source)}
		}
		return fmt.Errorf("get Secret %q: %w", internalSecretPrefix+source, err)
	}
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(dstName),
		Type:       src.Type,
		Data:       src.Data,
	}
	if err := controllerutil.SetControllerReference(c.Instance(), secret, c.Client().Scheme()); err != nil {
		return fmt.Errorf("set owner reference on Secret %q: %w", dstName, err)
	}
	if err := c.Client().Create(c.Context(), secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create Secret %q: %w", dstName, err)
	}
	return nil
}
