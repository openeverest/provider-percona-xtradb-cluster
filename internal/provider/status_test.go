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
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	statusTestName      = "my-pxc"
	statusTestNamespace = "db"
	statusTestUID       = types.UID("6f0c5a3e-cluster")
	statusTestImage     = "percona/percona-xtradb-cluster:8.4.8"
	statusTestHost      = "my-pxc-haproxy.db"
)

// statusContext builds a context for an Instance named my-pxc and seeds the
// cluster with objs, plus the users Secret the connection details are read
// from.
func statusContext(t *testing.T, spec corev1alpha1.InstanceSpec, objs ...client.Object) *controller.Context {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1alpha1.AddToScheme(scheme))
	require.NoError(t, backupv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, pxcv1.SchemeBuilder.AddToScheme(scheme))

	in := &corev1alpha1.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: statusTestName, Namespace: statusTestNamespace},
		Spec:       spec,
	}
	users := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-" + statusTestName, Namespace: statusTestNamespace},
		Data:       map[string][]byte{"root": []byte("secret")},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(append(objs, in, users)...).
		Build()

	return controller.NewContext(context.Background(), cl, in, "percona-xtradb-cluster")
}

// readyCluster returns a three-node cluster behind two HAProxy pods that the
// operator reports ready for its latest generation. Tests change what they
// are about.
func readyCluster(changes ...func(*pxcv1.PerconaXtraDBCluster)) *pxcv1.PerconaXtraDBCluster {
	pxc := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:       statusTestName,
			Namespace:  statusTestNamespace,
			UID:        statusTestUID,
			Generation: 3,
		},
		Spec: pxcv1.PerconaXtraDBClusterSpec{
			TLS: &pxcv1.TLSSpec{Enabled: ptrTo(true)},
			PXC: &pxcv1.PXCSpec{PodSpec: &pxcv1.PodSpec{
				Size:  3,
				Image: statusTestImage,
				VolumeSpec: &pxcv1.VolumeSpec{PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					}},
				}},
			}},
			HAProxy: &pxcv1.HAProxySpec{PodSpec: pxcv1.PodSpec{Enabled: true, Size: 2}},
		},
		Status: pxcv1.PerconaXtraDBClusterStatus{
			Status:             pxcv1.AppStateReady,
			ObservedGeneration: 3,
			Host:               statusTestHost,
			PXC: pxcv1.AppStatus{
				ComponentStatus: pxcv1.ComponentStatus{Status: pxcv1.AppStateReady, Image: statusTestImage, Version: "8.4.8-8.1"},
				Size:            3,
				Ready:           3,
			},
			HAProxy: pxcv1.AppStatus{
				ComponentStatus: pxcv1.ComponentStatus{Status: pxcv1.AppStateReady},
				Size:            2,
				Ready:           2,
			},
			Size:  5,
			Ready: 5,
			Conditions: []pxcv1.ClusterCondition{
				{Type: naming.ConditionTLS, Status: pxcv1.ConditionStatus(naming.ConditionTLSStateEnabled)},
				{Type: pxcv1.AppStateReady, Status: pxcv1.ConditionTrue},
			},
		},
	}
	for _, change := range changes {
		change(pxc)
	}
	return pxc
}

// proxySQLCluster switches the cluster from HAProxy to ProxySQL.
func proxySQLCluster(pxc *pxcv1.PerconaXtraDBCluster) {
	pxc.Spec.HAProxy = nil
	pxc.Spec.ProxySQL = &pxcv1.ProxySQLSpec{PodSpec: pxcv1.PodSpec{Enabled: true, Size: 2}}
	pxc.Status.HAProxy = pxcv1.AppStatus{}
	pxc.Status.ProxySQL = pxcv1.AppStatus{
		ComponentStatus: pxcv1.ComponentStatus{Status: pxcv1.AppStateReady},
		Size:            2,
		Ready:           2,
	}
	pxc.Status.Host = "my-pxc-proxysql.db"
}

func state(state pxcv1.AppState) func(*pxcv1.PerconaXtraDBCluster) {
	return func(pxc *pxcv1.PerconaXtraDBCluster) { pxc.Status.Status = state }
}

// nodes changes what the operator publishes about the PXC nodes.
func nodes(change func(*pxcv1.AppStatus)) func(*pxcv1.PerconaXtraDBCluster) {
	return func(pxc *pxcv1.PerconaXtraDBCluster) { change(&pxc.Status.PXC) }
}

// failing puts the cluster in the state the operator leaves it in when a pass
// over it fails: the error comes after what its last complete pass said
// about the pods.
func failing(message string) func(*pxcv1.PerconaXtraDBCluster) {
	return func(pxc *pxcv1.PerconaXtraDBCluster) {
		pxc.Status.Status = pxcv1.AppStateError
		pxc.Status.Messages = append(pxc.Status.Messages, "Error: "+message)
	}
}

// paused sets spec.pause and the state the operator reports once every pod
// is gone.
func paused(pxc *pxcv1.PerconaXtraDBCluster) {
	pxc.Spec.Pause = true
	pxc.Status.Status = pxcv1.AppStatePaused
	pxc.Status.PXC = pxcv1.AppStatus{ComponentStatus: pxcv1.ComponentStatus{Status: pxcv1.AppStatePaused, Image: statusTestImage}, Size: 3}
	pxc.Status.HAProxy = pxcv1.AppStatus{ComponentStatus: pxcv1.ComponentStatus{Status: pxcv1.AppStatePaused}, Size: 2}
}

// rolledOut returns one of the cluster's StatefulSets with every pod ready
// and running its latest template.
func rolledOut(component string, changes ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	set := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:       statusTestName + "-" + component,
			Namespace:  statusTestNamespace,
			Generation: 2,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: pxcv1.SchemeGroupVersion.String(),
				Kind:       "PerconaXtraDBCluster",
				Name:       statusTestName,
				UID:        statusTestUID,
				Controller: ptrTo(true),
			}},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:       ptrTo(int32(3)),
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		},
		Status: appsv1.StatefulSetStatus{
			ObservedGeneration: 2,
			Replicas:           3,
			ReadyReplicas:      3,
			UpdatedReplicas:    3,
			// With OnDelete the controller never moves currentRevision.
			CurrentRevision: statusTestName + "-" + component + "-1",
			UpdateRevision:  statusTestName + "-" + component + "-2",
		},
	}
	for _, change := range changes {
		change(set)
	}
	return set
}

// haproxySet is the HAProxy StatefulSet with two pods, rolled out.
func haproxySet(changes ...func(*appsv1.StatefulSet)) *appsv1.StatefulSet {
	return rolledOut("haproxy", append([]func(*appsv1.StatefulSet){func(set *appsv1.StatefulSet) {
		set.Spec.Replicas = ptrTo(int32(2))
		set.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType}
		set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
	}}, changes...)...)
}

func restoreInState(name string, state backupv1alpha1.RestoreState) *backupv1alpha1.Restore {
	return &backupv1alpha1.Restore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: statusTestNamespace},
		Spec:       backupv1alpha1.RestoreSpec{InstanceRef: commonv1alpha1.ObjectRef{Name: statusTestName}},
		Status:     backupv1alpha1.RestoreStatus{State: state},
	}
}

func operatorRestoreInState(name, cluster string, state pxcv1.RestoreState) *pxcv1.PerconaXtraDBClusterRestore {
	return &pxcv1.PerconaXtraDBClusterRestore{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: statusTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterRestoreSpec{PXCCluster: cluster, BackupName: "nightly"},
		Status:     pxcv1.PerconaXtraDBClusterRestoreStatus{State: state},
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()

	seededSpec := corev1alpha1.InstanceSpec{DataSource: &backupv1alpha1.DataSource{
		Type:   backupv1alpha1.DataSourceTypeBackup,
		Backup: &backupv1alpha1.DataSourceBackup{BackupRef: commonv1alpha1.ObjectRef{Name: "nightly"}},
	}}

	tests := []struct {
		name string
		spec corev1alpha1.InstanceSpec
		// cluster is the PerconaXtraDBCluster object; nil when it does not
		// exist.
		cluster *pxcv1.PerconaXtraDBCluster
		// sets are the cluster's StatefulSets; nil stands for the PXC and
		// HAProxy StatefulSets, rolled out.
		sets []*appsv1.StatefulSet
		// others are more objects in the namespace.
		others []client.Object

		converged bool
		// message is what the provider waits for, in part.
		message string
		// available, availableReason and availableMessage (in part) say
		// whether clients can connect.
		available        controller.Availability
		availableReason  string
		availableMessage string
		// problems are the reported problems, messages in part.
		problems []controller.Problem
		// operations are the reported operations, as "Type/Reason".
		operations []string
	}{
		{
			name:    "the cluster object does not exist yet",
			cluster: nil,
			message: "Waiting for the PerconaXtraDBCluster object to be created",
			// The provider cannot tell, so it leaves the answer to the runtime.
			available: controller.AvailabilityUnreported,
		},
		{
			name: "a new cluster the operator has not picked up",
			cluster: readyCluster(func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Generation = 1
				pxc.Status = pxcv1.PerconaXtraDBClusterStatus{}
			}),
			sets:      []*appsv1.StatefulSet{},
			message:   "Waiting for the operator to start creating the cluster",
			available: controller.AvailabilityUnreported,
		},
		{
			name: "a new cluster coming up",
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready = pxcv1.AppStateInit, 1
			}), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Status.HAProxy.Status, pxc.Status.HAProxy.Ready = pxcv1.AppStateInit, 0
			}),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) {
					set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 1, 2
				}),
				haproxySet(func(set *appsv1.StatefulSet) { set.Status.ReadyReplicas = 0 }),
			},
			message:          "Waiting for PXC nodes (1 of 3 ready); HAProxy (0 of 2 ready)",
			available:        controller.Unavailable,
			availableReason:  reasonNoProxyReady,
			availableMessage: "No HAProxy pod is ready",
		},
		{
			name: "a new cluster with no node ready",
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready, st.Image = pxcv1.AppStateInit, 0, ""
			})),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.ReadyReplicas = 0 }),
				haproxySet(),
			},
			message:          "Waiting for PXC nodes (0 of 3 ready)",
			available:        controller.Unavailable,
			availableReason:  reasonNoNodeReady,
			availableMessage: "No PXC node is ready",
		},
		{
			name:      "ready and caught up",
			cluster:   readyCluster(),
			converged: true,
			available: controller.Available,
		},
		{
			name:    "ready behind ProxySQL",
			cluster: readyCluster(proxySQLCluster),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc"),
				rolledOut("proxysql", func(set *appsv1.StatefulSet) {
					set.Spec.Replicas = ptrTo(int32(2))
					set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 2, 2, 2
				}),
			},
			converged: true,
			available: controller.Available,
		},
		{
			name: "no ProxySQL pod ready",
			cluster: readyCluster(proxySQLCluster, state(pxcv1.AppStateInit), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Status.ProxySQL.Status, pxc.Status.ProxySQL.Ready = pxcv1.AppStateInit, 0
			}),
			sets:             []*appsv1.StatefulSet{rolledOut("pxc")},
			message:          "Waiting for ProxySQL (0 of 2 ready)",
			available:        controller.Unavailable,
			availableReason:  reasonNoProxyReady,
			availableMessage: "No ProxySQL pod is ready",
		},
		{
			name: "ready but the operator has not seen the latest generation",
			cluster: readyCluster(func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Generation = 4
			}),
			message:   "Waiting for the operator to apply the latest configuration",
			available: controller.Available,
		},
		{
			name:    "ready but a StatefulSet has not picked up its new pod template",
			cluster: readyCluster(),
			sets: []*appsv1.StatefulSet{
				haproxySet(),
				rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Generation = 3 }),
			},
			message:   "Waiting for StatefulSet my-pxc-pxc to pick up its new pod template",
			available: controller.Available,
		},
		{
			name:    "ready but the rolling restart has not finished",
			cluster: readyCluster(),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 1 }),
				haproxySet(),
			},
			message:   "Waiting for StatefulSet my-pxc-pxc to roll out (1 of 3 pods updated)",
			available: controller.Available,
		},
		{
			name:    "ready but a proxy pod is not ready yet",
			cluster: readyCluster(),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc"),
				haproxySet(func(set *appsv1.StatefulSet) { set.Status.ReadyReplicas = 1 }),
			},
			message:   "Waiting for StatefulSet my-pxc-haproxy to become ready (1 of 2 pods ready)",
			available: controller.Available,
		},
		{
			name: "a rolling update before the first node has restarted",
			// The operator counts only the nodes on the latest pod template.
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready = pxcv1.AppStateInit, 0
			})),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) {
					set.Status.UpdatedReplicas, set.Status.ReadyReplicas = 0, 2
				}),
				haproxySet(),
			},
			message:          "Waiting for StatefulSet my-pxc-pxc to roll out (0 of 3 pods updated)",
			available:        controller.AvailabilityUnknown,
			availableReason:  reasonReadyNodesNotCounted,
			availableMessage: "none does yet",
		},
		{
			name: "a rolling update past its first node",
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready = pxcv1.AppStateInit, 1
			})),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 1 }),
				haproxySet(),
			},
			message:   "Waiting for StatefulSet my-pxc-pxc to roll out (1 of 3 pods updated)",
			available: controller.Available,
		},
		{
			name: "the operator reports why a pod cannot start",
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready = pxcv1.AppStateInit, 2
				st.Message = "0/3 nodes are available: 3 Insufficient memory.; "
			})),
			message:   "Waiting for PXC nodes (0/3 nodes are available: 3 Insufficient memory.)",
			available: controller.Available,
		},
		{
			name: "ready but the load balancer has no address yet",
			cluster: readyCluster(state(pxcv1.AppStateInit), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Status.Host = ""
			}),
			message:   "Waiting for the operator to report the cluster ready (it reports initializing)",
			available: controller.Available,
		},
		{
			name: "a pod that restarts slowly fails the operator's pass",
			cluster: readyCluster(failing(
				"pxc upgrade error: smart update: failed to apply changes: failed to wait pod: reach pod wait limit")),
			message:   "Waiting for a pod to restart",
			available: controller.Available,
		},
		{
			name: "a write conflict fails the operator's pass",
			cluster: readyCluster(failing(
				`reconcile users: update internal secret: Operation cannot be fulfilled on secrets "internal-my-pxc": ` +
					"the object has been modified; please apply your changes to the latest version and try again")),
			message:   "Waiting for the operator to retry a conflicting write",
			available: controller.Available,
		},
		{
			name:      "upstream error",
			cluster:   readyCluster(failing("PXC service upgrade error: create service: services is forbidden")),
			message:   "The operator reports an error: PXC service upgrade error",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  corev1alpha1.ReasonEngineError,
				Message: "PXC service upgrade error: create service: services is forbidden",
			}},
		},
		{
			name: "upstream error after messages about the pods",
			cluster: readyCluster(func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Status.Messages = []string{"pxc: pxc: back-off 5m0s restarting failed container; "}
			}, failing("failed to deploy pxc: updatePod for pxc: reconcile config: get configmap: timeout")),
			message:   "The operator reports an error: failed to deploy pxc",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  corev1alpha1.ReasonEngineError,
				Message: "failed to deploy pxc: updatePod for pxc: reconcile config: get configmap: timeout",
			}},
		},
		{
			name: "upstream error from rejected credentials",
			cluster: readyCluster(failing("manage sys users: get pxc users: connect to pxc: " +
				"Error 1045 (28000): Access denied for user 'operator'@'10.42.0.12' (using password: YES)")),
			message:   "The operator reports an error",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  reasonAuthenticationFailed,
				Message: "Access denied for user 'operator'",
			}},
		},
		{
			name: "upstream error on a generation the operator has not caught up with",
			cluster: readyCluster(failing("check safe defaults: PXC size must be at least 3. "+
				"Set spec.unsafeFlags.pxcSize to true to disable this check"),
				func(pxc *pxcv1.PerconaXtraDBCluster) { pxc.Generation = 4 }),
			message:   "The operator reports an error: check safe defaults",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  reasonConfigurationRejected,
				Message: "PXC size must be at least 3",
			}},
		},
		{
			name: "upstream error about certificates",
			cluster: readyCluster(failing(`create ssl with cert manager create certificate: issuers.cert-manager.io "corp-ca" not found`),
				nodes(func(st *pxcv1.AppStatus) { st.Ready = 0 })),
			sets:             []*appsv1.StatefulSet{},
			message:          "The operator reports an error: create ssl with cert manager",
			available:        controller.Unavailable,
			availableReason:  reasonNoNodeReady,
			availableMessage: "No PXC node is ready",
			problems: []controller.Problem{{
				Reason:  reasonTLSCertificatesNotReady,
				Message: `"corp-ca" not found`,
			}},
		},
		{
			name: "volumes that could not be resized",
			cluster: readyCluster(failing("datadir-my-pxc-pxc-0 pvc resize failed: VolumeResizeFailed: " +
				"resize volume failed: storage class does not support expansion")),
			message:   "The operator reports an error",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  corev1alpha1.ReasonStorageResizeFailed,
				Message: "pvc resize failed: VolumeResizeFailed",
			}},
		},
		{
			name:      "a crash loop during a rolling update",
			cluster:   readyCluster(failing("pxc upgrade error: smart update: failed to apply changes: failed to wait pod: pod my-pxc-pxc-2 is in CrashLoopBackOff state")),
			message:   "The operator reports an error",
			available: controller.Available,
			problems: []controller.Problem{{
				Reason:  corev1alpha1.ReasonCrashLoopBackOff,
				Message: "pod my-pxc-pxc-2 is in CrashLoopBackOff state",
			}},
		},
		{
			name:    "paused upstream",
			cluster: readyCluster(paused),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) {
					set.Spec.Replicas = ptrTo(int32(0))
					set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 0, 0, 0
				}),
			},
			message:          "The cluster is paused on its PerconaXtraDBCluster object",
			available:        controller.Unavailable,
			availableReason:  reasonPaused,
			availableMessage: "The cluster is paused",
			problems: []controller.Problem{{
				Reason:   reasonPausedOutsideOpenEverest,
				Message:  "spec.pause is set on its PerconaXtraDBCluster object",
				Terminal: true,
			}},
		},
		{
			name: "pause requested upstream, pods still running",
			cluster: readyCluster(func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Spec.Pause = true
				pxc.Generation = 4
			}),
			message:         "The cluster is paused on its PerconaXtraDBCluster object",
			available:       controller.Unavailable,
			availableReason: reasonPaused,
			problems: []controller.Problem{{
				Reason:   reasonPausedOutsideOpenEverest,
				Message:  "Unset it to start the database again",
				Terminal: true,
			}},
		},
		{
			name: "paused by the operator to turn TLS off",
			cluster: readyCluster(paused, func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Spec.TLS.Enabled = ptrTo(false)
			}),
			sets:             []*appsv1.StatefulSet{},
			message:          "Waiting for the operator to restart the cluster with TLS disabled",
			available:        controller.Unavailable,
			availableReason:  reasonPaused,
			availableMessage: "The operator paused the cluster to restart it with TLS disabled",
		},
		{
			name: "starting again after a pause",
			cluster: readyCluster(state(pxcv1.AppStatePaused), nodes(func(st *pxcv1.AppStatus) {
				st.Status, st.Ready = pxcv1.AppStatePaused, 0
			})),
			sets:             []*appsv1.StatefulSet{},
			message:          "Waiting for PXC nodes (0 of 3 ready)",
			available:        controller.Unavailable,
			availableReason:  reasonPaused,
			availableMessage: "The cluster is starting again after a pause",
		},
		{
			name:             "a restore running",
			cluster:          readyCluster(paused),
			sets:             []*appsv1.StatefulSet{},
			others:           []client.Object{restoreInState("nightly", backupv1alpha1.RestoreStateRunning)},
			message:          "Restore nightly is running",
			available:        controller.Unavailable,
			availableReason:  reasonRestoring,
			availableMessage: "A restore is replacing the data",
			operations:       []string{"Restoring/Restoring"},
		},
		{
			name: "a restore running while the operator cannot reach the nodes",
			cluster: readyCluster(failing("manage sys users: get pxc users: connect to pxc: dial tcp: connection refused"),
				func(pxc *pxcv1.PerconaXtraDBCluster) { pxc.Spec.Pause = true }),
			others:          []client.Object{restoreInState("nightly", backupv1alpha1.RestoreStatePending)},
			message:         "Restore nightly is running",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
			operations:      []string{"Restoring/Restoring"},
		},
		{
			name:    "restores that have ended",
			cluster: readyCluster(),
			others: []client.Object{
				restoreInState("nightly", backupv1alpha1.RestoreStateSucceeded),
				restoreInState("weekly", backupv1alpha1.RestoreStateFailed),
				operatorRestoreInState("nightly", statusTestName, pxcv1.RestoreSucceeded),
				operatorRestoreInState("weekly", statusTestName, pxcv1.RestoreFailed),
			},
			converged: true,
			available: controller.Available,
		},
		{
			name:             "a restore created on the operator directly",
			cluster:          readyCluster(paused),
			sets:             []*appsv1.StatefulSet{},
			others:           []client.Object{operatorRestoreInState("by-hand", statusTestName, pxcv1.RestoreRestore)},
			message:          "PerconaXtraDBClusterRestore by-hand is running",
			available:        controller.Unavailable,
			availableReason:  reasonRestoring,
			availableMessage: "A restore is replacing the data",
			operations:       []string{"Restoring/Restoring"},
		},
		{
			name:    "operator restores that have not started, or are for another cluster",
			cluster: readyCluster(),
			others: []client.Object{
				operatorRestoreInState("queued", statusTestName, pxcv1.RestoreNew),
				operatorRestoreInState("elsewhere", "other-pxc", pxcv1.RestoreRestore),
			},
			converged: true,
			available: controller.Available,
		},
		{
			// The runtime reports the seeding of a new Instance itself.
			name:            "a new instance being seeded",
			spec:            seededSpec,
			cluster:         readyCluster(paused),
			sets:            []*appsv1.StatefulSet{},
			others:          []client.Object{restoreInState("my-pxc-datasource", backupv1alpha1.RestoreStateRunning)},
			message:         "Restore my-pxc-datasource is loading the data source",
			available:       controller.Unavailable,
			availableReason: reasonRestoring,
		},
		{
			name:      "a new instance that has been seeded",
			spec:      seededSpec,
			cluster:   readyCluster(),
			others:    []client.Object{restoreInState("my-pxc-datasource", backupv1alpha1.RestoreStateSucceeded)},
			converged: true,
			available: controller.Available,
		},
		{
			name: "a new engine image rolling out",
			cluster: readyCluster(state(pxcv1.AppStateInit), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Spec.PXC.Image = "percona/percona-xtradb-cluster:8.4.9"
			}),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 1 }),
				haproxySet(),
			},
			message:    "Waiting for StatefulSet my-pxc-pxc to roll out (1 of 3 pods updated)",
			available:  controller.Available,
			operations: []string{"Upgrading/RollingOutImage"},
		},
		{
			name: "volumes being resized",
			cluster: readyCluster(state(pxcv1.AppStateInit), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Annotations = map[string]string{pxcv1.AnnotationPVCResizeInProgress: "2026-11-19T10:00:00Z"}
			}),
			message:    "Waiting for the volumes of the PXC nodes to be resized to 20Gi",
			available:  controller.Available,
			operations: []string{"StorageResizing/ExpansionTriggered"},
		},
		{
			name:    "StatefulSets of another cluster are ignored",
			cluster: readyCluster(),
			sets: []*appsv1.StatefulSet{
				rolledOut("pxc"),
				haproxySet(),
				rolledOut("other", func(set *appsv1.StatefulSet) {
					set.OwnerReferences[0].UID = "another-cluster"
					set.Status.UpdatedReplicas = 0
				}),
			},
			converged: true,
			available: controller.Available,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objs := append([]client.Object{}, tt.others...)
			if tt.cluster != nil {
				objs = append(objs, tt.cluster)
			}
			sets := tt.sets
			if sets == nil {
				sets = []*appsv1.StatefulSet{rolledOut("pxc"), haproxySet()}
			}
			for _, set := range sets {
				objs = append(objs, set)
			}

			got, err := StatusPXC(statusContext(t, tt.spec, objs...))
			require.NoError(t, err)

			assert.Equal(t, tt.converged, got.Converged, "converged; message: %s", got.Message)
			if tt.message == "" {
				assert.Empty(t, got.Message)
			} else {
				assert.Contains(t, got.Message, tt.message)
			}

			assert.Equal(t, tt.available, got.Available, "available: %s", got.AvailableMessage)
			assert.Equal(t, tt.availableReason, got.AvailableReason)
			if tt.availableMessage == "" && tt.available != controller.Unavailable {
				assert.Empty(t, got.AvailableMessage)
			} else {
				assert.Contains(t, got.AvailableMessage, tt.availableMessage)
			}

			require.Len(t, got.Problems, len(tt.problems), "problems: %v", got.Problems)
			for i, want := range tt.problems {
				assert.Equal(t, want.Reason, got.Problems[i].Reason)
				assert.Equal(t, want.Terminal, got.Problems[i].Terminal, "terminal")
				assert.Contains(t, got.Problems[i].Message, want.Message)
			}

			var operations []string
			for _, op := range got.Operations {
				operations = append(operations, op.Type+"/"+op.Reason)
				assert.NotEmpty(t, op.Message)
			}
			assert.Equal(t, tt.operations, operations)

			// Connection details are reported while clients can connect,
			// converged or not, and never for a database that is down.
			if tt.converged || (tt.available == controller.Available && tt.cluster.Status.Host != "") {
				assert.Equal(t, "mysql", got.ConnectionDetails.Type)
				assert.Equal(t, tt.cluster.Status.Host, got.ConnectionDetails.Host)
				assert.Equal(t, "root", got.ConnectionDetails.Username)
			} else {
				assert.True(t, got.ConnectionDetails.IsEmpty())
			}
		})
	}
}

// TestStatusWithoutUsersSecret covers a cluster clients can connect to before
// its users Secret can be read: the provider fails the pass so that the
// runtime retries it, since nothing else would.
func TestStatusWithoutUsersSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cluster *pxcv1.PerconaXtraDBCluster
		wantErr bool
	}{
		{name: "ready", cluster: readyCluster(), wantErr: true},
		{name: "available while a change rolls out", cluster: readyCluster(state(pxcv1.AppStateInit)), wantErr: true},
		{
			name:    "not available",
			cluster: readyCluster(state(pxcv1.AppStateInit), nodes(func(st *pxcv1.AppStatus) { st.Ready = 0 })),
		},
		{
			name: "available but the operator has not published a host",
			cluster: readyCluster(state(pxcv1.AppStateInit), func(pxc *pxcv1.PerconaXtraDBCluster) {
				pxc.Status.Host = ""
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := statusContext(t, corev1alpha1.InstanceSpec{}, tt.cluster, rolledOut("pxc"), haproxySet())
			users := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "everest-secrets-" + statusTestName, Namespace: statusTestNamespace}}
			require.NoError(t, c.Client().Delete(context.Background(), users))

			got, err := StatusPXC(c)
			if tt.wantErr {
				require.ErrorContains(t, err, "build connection details")
				return
			}
			require.NoError(t, err)
			assert.True(t, got.ConnectionDetails.IsEmpty())
		})
	}
}

func TestUpstreamErrorReason(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"manage sys users: Error 1045 (28000): Access denied for user 'root'@'localhost' (using password: YES)":    reasonAuthenticationFailed,
		"validate cr: spec.pxc section is not specified. Please check my-pxc cluster settings":                     reasonConfigurationRejected,
		"check safe defaults: PXC size must be an odd number. Set spec.unsafeFlags.pxcSize to true":                reasonConfigurationRejected,
		"pxc config: resources.limits[memory] should be specified for template usage in configuration":             reasonConfigurationRejected,
		"haproxy config: parse template: unexpected token":                                                         reasonConfigurationRejected,
		"proxySQL config: parse template: unexpected token":                                                        reasonConfigurationRejected,
		"requested storage (10Gi) is less than actual storage (20Gi)":                                              reasonConfigurationRejected,
		"create ssl internally: create CA certificate: key too short":                                              reasonTLSCertificatesNotReady,
		`create ssl with cert manager timeout: can't get tls certificates from certmanager, [my-pxc-ssl]`:          reasonTLSCertificatesNotReady,
		"datadir-my-pxc-pxc-1 pvc resize failed: ExceededQuota: PVC resize failed":                                 corev1alpha1.ReasonStorageQuotaExceeded,
		"datadir-my-pxc-pxc-1 pvc resize failed: StorageClassNotSupportResize: PVC resize failed":                  corev1alpha1.ReasonStorageResizeFailed,
		"smart update: failed to apply changes: failed to wait pod: pod my-pxc-pxc-1 is in CrashLoopBackOff state": corev1alpha1.ReasonCrashLoopBackOff,
		"smart update: failed to apply changes: failed to wait pod: pod my-pxc-pxc-1 is in ErrImagePull state":     corev1alpha1.ReasonImagePullBackOff,
		"smart update: failed to apply changes: failed to wait pod: pod my-pxc-pxc-1 is in ImagePullBackOff state": corev1alpha1.ReasonImagePullBackOff,
		"smart update: failed to apply changes: failed to wait pod: pod my-pxc-pxc-1 is not scheduled: 0/3 nodes":  corev1alpha1.ReasonUnschedulable,
		"reconcile backups: create cronjob: forbidden":                                                             corev1alpha1.ReasonEngineError,
		"": corev1alpha1.ReasonEngineError,
	}
	for message, want := range tests {
		t.Run(want+"/"+message, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, upstreamErrorReason(message))
		})
	}
}

func TestRolloutWait(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sets []*appsv1.StatefulSet
		// rollout is what rolloutWait says, pods what podsWait says.
		rollout, pods string
	}{
		{name: "no StatefulSets"},
		{
			name: "every pod runs the latest template",
			sets: []*appsv1.StatefulSet{rolledOut("pxc"), haproxySet()},
		},
		{
			name:    "the controller has not seen the latest template",
			sets:    []*appsv1.StatefulSet{rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Generation = 3 })},
			rollout: "StatefulSet my-pxc-pxc to pick up its new pod template",
		},
		{
			name:    "pods on the previous template",
			sets:    []*appsv1.StatefulSet{rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.UpdatedReplicas = 0 })},
			rollout: "StatefulSet my-pxc-pxc to roll out (0 of 3 pods updated)",
		},
		{
			name: "scaling up",
			sets: []*appsv1.StatefulSet{rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Spec.Replicas = ptrTo(int32(5)) })},
			pods: "StatefulSet my-pxc-pxc to scale to 5 pods (it has 3)",
		},
		{
			name: "a pod not ready",
			sets: []*appsv1.StatefulSet{rolledOut("pxc", func(set *appsv1.StatefulSet) { set.Status.ReadyReplicas = 2 })},
			pods: "StatefulSet my-pxc-pxc to become ready (2 of 3 pods ready)",
		},
		{
			name: "one being removed",
			sets: []*appsv1.StatefulSet{rolledOut("proxysql", func(set *appsv1.StatefulSet) {
				now := metav1.Now()
				set.DeletionTimestamp = &now
				set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 0, 0
			})},
		},
		{
			name: "replicas unset",
			sets: []*appsv1.StatefulSet{rolledOut("pxc", func(set *appsv1.StatefulSet) {
				set.Spec.Replicas = nil
				set.Status.Replicas, set.Status.ReadyReplicas, set.Status.UpdatedReplicas = 1, 1, 1
			})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sets := make([]appsv1.StatefulSet, 0, len(tt.sets))
			for _, set := range tt.sets {
				sets = append(sets, *set)
			}
			assert.Equal(t, tt.rollout, rolloutWait(sets))
			assert.Equal(t, tt.pods, podsWait(sets))
		})
	}
}

func TestEnqueueStatefulSetInstance(t *testing.T) {
	t.Parallel()

	controllerRef := func(apiVersion, kind string) []metav1.OwnerReference {
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: statusTestName, Controller: ptrTo(true)}}
	}

	tests := []struct {
		name   string
		owners []metav1.OwnerReference
		want   []reconcile.Request
	}{
		{
			name:   "a StatefulSet of a cluster",
			owners: controllerRef(pxcv1.SchemeGroupVersion.String(), "PerconaXtraDBCluster"),
			want: []reconcile.Request{{
				NamespacedName: types.NamespacedName{Namespace: statusTestNamespace, Name: statusTestName},
			}},
		},
		{
			name:   "a StatefulSet something else controls",
			owners: controllerRef("apps.example.com/v1", "PerconaXtraDBCluster"),
		},
		{
			name: "a StatefulSet the cluster owns without controlling it",
			owners: []metav1.OwnerReference{{
				APIVersion: pxcv1.SchemeGroupVersion.String(), Kind: "PerconaXtraDBCluster", Name: statusTestName,
			}},
		},
		{name: "a StatefulSet without owners"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			set := &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{Name: "some-set", Namespace: statusTestNamespace, OwnerReferences: tt.owners},
			}
			assert.Equal(t, tt.want, enqueueStatefulSetInstance()(context.Background(), set))
		})
	}
}
