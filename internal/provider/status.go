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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/percona/percona-xtradb-cluster-operator/pkg/naming"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Reasons of the problems the provider reports. A problem's reason becomes
// the status word users read, so each names one cause.
const (
	// reasonPausedOutsideOpenEverest: spec.pause is set on the
	// PerconaXtraDBCluster object, and neither a restore nor a TLS toggle
	// explains it. An Instance cannot ask for that, so it was set there by
	// hand.
	reasonPausedOutsideOpenEverest = "PausedOutsideOpenEverest"
	// reasonAuthenticationFailed: MySQL rejects the credentials the
	// operator connects with.
	reasonAuthenticationFailed = "AuthenticationFailed"
	// reasonConfigurationRejected: the operator refuses the cluster's
	// configuration.
	reasonConfigurationRejected = "ConfigurationRejected"
	// reasonTLSCertificatesNotReady: the operator cannot get the cluster's
	// certificates.
	reasonTLSCertificatesNotReady = "TLSCertificatesNotReady"
)

// Reasons explaining why clients cannot connect, or why that is unknown.
const (
	reasonPaused       = "Paused"
	reasonRestoring    = corev1alpha1.ConditionRestoring
	reasonNoNodeReady  = "NoNodeReady"
	reasonNoProxyReady = "NoProxyReady"
	// reasonReadyNodesNotCounted: the operator counts only the nodes that
	// run the latest pod template, and none does yet.
	reasonReadyNodesNotCounted = "ReadyNodesNotCounted"
)

// reasonRollingOutImage is the reason of the Upgrading operation.
const reasonRollingOutImage = "RollingOutImage"

// Fragments of operator error messages the provider recognizes.
const (
	// The operator gives a restarted pod a fixed time to become ready, fails
	// its pass when the pod takes longer, and picks the rolling update up
	// again on the next pass.
	errFragmentPodWaitLimit = "reach pod wait limit"
	// The API server refuses a write made from a stale read with this
	// message. The operator fails its pass on one and retries at once.
	errFragmentConflict = "Operation cannot be fulfilled"
)

// transientUpstreamErrors are the operator errors that are no failure: the
// operator carries on with its next pass. Each comes with what the cluster
// is waiting for meanwhile.
var transientUpstreamErrors = []struct{ fragment, wait string }{
	{errFragmentPodWaitLimit, "Waiting for a pod to restart"},
	{errFragmentConflict, "Waiting for the operator to retry a conflicting write"},
}

// upstreamErrorReasons names the operator errors a user can act on, by a
// fragment of their message. The first match wins.
var upstreamErrorReasons = []struct{ fragment, reason string }{
	// MySQL's own message for rejected credentials, which the operator
	// passes on when the users Secret does not match the data.
	{"Access denied for user", reasonAuthenticationFailed},
	// The operator checks the PerconaXtraDBCluster before it acts on it.
	{"validate cr: ", reasonConfigurationRejected},
	{"check safe defaults: ", reasonConfigurationRejected},
	{"pxc config: ", reasonConfigurationRejected},
	{"haproxy config: ", reasonConfigurationRejected},
	{"proxySQL config: ", reasonConfigurationRejected},
	{"is less than actual storage", reasonConfigurationRejected},
	{"create ssl with cert manager", reasonTLSCertificatesNotReady},
	{"create ssl internally", reasonTLSCertificatesNotReady},
	{"can't get tls certificates", reasonTLSCertificatesNotReady},
	{"pvc resize failed: " + naming.EventExceededQuota, corev1alpha1.ReasonStorageQuotaExceeded},
	{"pvc resize failed", corev1alpha1.ReasonStorageResizeFailed},
	// A pod the operator waits for during a rolling update cannot start.
	{"CrashLoopBackOff", corev1alpha1.ReasonCrashLoopBackOff},
	{"ImagePullBackOff", corev1alpha1.ReasonImagePullBackOff},
	{"ErrImagePull", corev1alpha1.ReasonImagePullBackOff},
	{"is not scheduled", corev1alpha1.ReasonUnschedulable},
}

// pauseCause says who paused the cluster, if anyone did.
type pauseCause int

const (
	notPaused pauseCause = iota
	// pausedForRestore: the operator stops the cluster while a restore
	// replaces its data, and starts it again afterwards.
	pausedForRestore
	// pausedForTLSToggle: the operator stops the cluster to turn TLS on or
	// off, and starts it again afterwards.
	pausedForTLSToggle
	// pausedOutside: nothing OpenEverest does will start the cluster again.
	pausedOutside
)

// restoreInProgress describes a restore that owns the cluster for now.
type restoreInProgress struct {
	// message says which restore runs.
	message string
	// seeding is true for the restore that fills a new Instance from its
	// data source. The runtime reports that one itself.
	seeding bool
}

// StatusPXC reports what the provider observes about the cluster: whether it
// runs what the Instance asks for, whether clients can connect, what is wrong
// and which long operation is running. The runtime derives the Instance's
// phase and conditions from these facts.
func StatusPXC(c *controller.Context) (controller.Status, error) {
	pxc, err := uncachedCluster(c)
	if apierrors.IsNotFound(err) {
		return controller.Progressing("Waiting for the PerconaXtraDBCluster object to be created"), nil
	}
	if err != nil {
		return controller.Status{}, err
	}

	restore, err := runningRestore(c)
	if err != nil {
		return controller.Status{}, err
	}

	sets, err := clusterStatefulSets(c, pxc)
	if err != nil {
		return controller.Status{}, err
	}

	status := observe(pxc, sets, restore)

	// The connection details are reported as soon as clients can connect,
	// not only once the cluster has converged: whoever is told the database
	// is available must find where to connect, and the details must follow a
	// change that is still rolling out.
	if !status.Converged && (status.Available != controller.Available || pxc.Status.Host == "") {
		return status, nil
	}

	details, err := buildConnectionDetails(c, pxc)
	if err != nil {
		// The users Secret is written by the operator, so its absence is
		// expected to pass. Returning the error has the runtime retry, which
		// nothing else would trigger: the provider does not watch Secrets.
		return controller.Status{}, fmt.Errorf("build connection details: %w", err)
	}
	status.ConnectionDetails = details
	return status, nil
}

// uncachedCluster reads the PerconaXtraDBCluster object from the API server
// instead of the cache. Sync has just applied it, and the cache may still
// hold the object from before that: its status would then look caught up with
// a generation that is no longer the latest, and the Instance would read as
// ready for a change the operator has not seen. The client reads unstructured
// objects without the cache.
func uncachedCluster(c *controller.Context) (*pxcv1.PerconaXtraDBCluster, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(pxcv1.SchemeGroupVersion.WithKind("PerconaXtraDBCluster"))
	if err := c.Get(obj, c.Name()); err != nil {
		return nil, fmt.Errorf("get PerconaXtraDBCluster %q: %w", c.Name(), err)
	}

	raw, err := obj.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("encode PerconaXtraDBCluster %q: %w", c.Name(), err)
	}
	pxc := &pxcv1.PerconaXtraDBCluster{}
	if err := json.Unmarshal(raw, pxc); err != nil {
		return nil, fmt.Errorf("decode PerconaXtraDBCluster %q: %w", c.Name(), err)
	}
	return pxc, nil
}

// runningRestore returns the restore that is in progress for the Instance, or
// nil. A restore stops the cluster, replaces its data and starts it again, so
// the restore reaching a terminal state is what ends it, not what the engine
// reports meanwhile.
func runningRestore(c *controller.Context) (*restoreInProgress, error) {
	restores, err := activeRestores(c, c.Namespace(), c.Name())
	if err != nil {
		return nil, err
	}
	slices.SortFunc(restores, func(a, b backupv1alpha1.Restore) int { return strings.Compare(a.Name, b.Name) })

	var seeding *restoreInProgress
	for i := range restores {
		r := &restores[i]
		if c.Instance().Spec.DataSource != nil && r.Name == c.Name()+controller.DataSourceRestoreNameSuffix {
			seeding = &restoreInProgress{message: fmt.Sprintf("Restore %s is loading the data source", r.Name), seeding: true}
			continue
		}
		return &restoreInProgress{message: fmt.Sprintf("Restore %s is running", r.Name)}, nil
	}
	if seeding != nil {
		return seeding, nil
	}

	// The operator also restores from a PerconaXtraDBClusterRestore created
	// on it directly, and stops the cluster for it the same way.
	opRestores := &pxcv1.PerconaXtraDBClusterRestoreList{}
	if err := c.List(opRestores); err != nil {
		return nil, fmt.Errorf("list PerconaXtraDBClusterRestore resources for cluster %q: %w", c.Name(), err)
	}
	var names []string
	for i := range opRestores.Items {
		r := &opRestores.Items[i]
		if r.Spec.PXCCluster != c.Name() || !r.DeletionTimestamp.IsZero() {
			continue
		}
		switch r.Status.State {
		case pxcv1.RestoreNew, pxcv1.RestoreSucceeded, pxcv1.RestoreFailed:
		default:
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	return &restoreInProgress{message: fmt.Sprintf("PerconaXtraDBClusterRestore %s is running", slices.Min(names))}, nil
}

// clusterStatefulSets returns the StatefulSets the operator runs for the
// cluster: the PXC nodes and the proxies. The operator labels their pods,
// not them, so they are found by their controller.
func clusterStatefulSets(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) ([]appsv1.StatefulSet, error) {
	list := &appsv1.StatefulSetList{}
	if err := c.List(list); err != nil {
		return nil, fmt.Errorf("list StatefulSets of cluster %q: %w", pxc.Name, err)
	}
	sets := slices.DeleteFunc(list.Items, func(set appsv1.StatefulSet) bool {
		return !metav1.IsControlledBy(&set, pxc)
	})
	slices.SortFunc(sets, func(a, b appsv1.StatefulSet) int { return strings.Compare(a.Name, b.Name) })
	return sets, nil
}

// observe turns the PerconaXtraDBCluster object, the cluster's StatefulSets
// and the restore in progress, if any, into the facts the provider reports.
func observe(pxc *pxcv1.PerconaXtraDBCluster, sets []appsv1.StatefulSet, restore *restoreInProgress) controller.Status {
	pause := pausedBy(pxc, restore)

	var status controller.Status
	status.Available, status.AvailableReason, status.AvailableMessage = availability(pxc, sets, restore, pause)

	if pause == pausedOutside {
		// The provider never sets spec.pause and does not own the field, so
		// its Sync leaves it alone: only whoever set it can start the
		// database again.
		status = status.WithTerminalProblem(reasonPausedOutsideOpenEverest,
			"The cluster is paused: spec.pause is set on its PerconaXtraDBCluster object, outside OpenEverest. Unset it to start the database again.")
	}

	// A restore stops the cluster, and the operator fails passes for as long
	// as it cannot reach it. Those errors come with the restore; one that
	// outlives it is reported once the restore has ended.
	if message, failing := upstreamError(pxc); failing && restore == nil {
		// Never terminal, even for an error only a person can fix: the
		// operator does not say which generation its error is about, so
		// right after a fix is applied the error still showing may be the
		// one being fixed, and the Instance must not read as stalled then.
		status = status.WithProblem(upstreamErrorReason(message), message)
	}

	if restore != nil && !restore.seeding {
		status = status.WithOperation(corev1alpha1.ConditionRestoring, reasonRestoring, restore.message)
	}

	// The operator marks the object while it grows the volumes of the PXC
	// nodes, and removes the mark once they all have the requested size.
	if pxc.PVCResizeInProgress() {
		status = status.WithOperation(corev1alpha1.ConditionStorageResizing,
			corev1alpha1.ReasonStorageExpansionTriggered, "Resizing the volumes of the PXC nodes to "+requestedStorage(pxc))
	}

	status.Message = waitingFor(pxc, sets, restore, pause)
	status.Converged = status.Message == ""

	// The operator records the image the nodes run only once they all run
	// the latest pod template, so a different image in the spec means an
	// engine version change is rolling out or about to.
	if running, wanted := pxc.Status.PXC.Image, engineImage(pxc); !status.Converged && running != "" && running != wanted {
		message := "Rolling out image " + wanted
		if version := pxc.Status.PXC.Version; version != "" {
			message += fmt.Sprintf(" (the cluster was running version %s)", version)
		}
		status = status.WithOperation(corev1alpha1.ConditionUpgrading, reasonRollingOutImage, message)
	}

	return status
}

// pausedBy says who paused the cluster. The operator pauses it itself to
// restore it or to turn TLS on or off, and starts it again afterwards; a
// pause neither explains was set outside OpenEverest.
func pausedBy(pxc *pxcv1.PerconaXtraDBCluster, restore *restoreInProgress) pauseCause {
	switch {
	case !pxc.Spec.Pause:
		return notPaused
	case restore != nil:
		return pausedForRestore
	case tlsToggleInProgress(pxc):
		return pausedForTLSToggle
	default:
		return pausedOutside
	}
}

// tlsToggleInProgress reports whether the operator is turning TLS on or off.
// It records the TLS state it last applied in a condition of its own, and
// stops the cluster until that state matches spec.tls.enabled.
func tlsToggleInProgress(pxc *pxcv1.PerconaXtraDBCluster) bool {
	cond := pxc.Status.FindCondition(naming.ConditionTLS)
	return cond != nil && naming.ConditionTLSState(cond.Status) != wantedTLSState(pxc)
}

// wantedTLSState is the TLS state spec.tls.enabled asks for. The operator
// enables TLS when the field is unset.
func wantedTLSState(pxc *pxcv1.PerconaXtraDBCluster) naming.ConditionTLSState {
	if pxc.Spec.TLS != nil && pxc.Spec.TLS.Enabled != nil && !*pxc.Spec.TLS.Enabled {
		return naming.ConditionTLSStateDisabled
	}
	return naming.ConditionTLSStateEnabled
}

// upstreamError returns the error the operator reports for its last pass
// over the cluster, and whether it counts as a failure.
func upstreamError(pxc *pxcv1.PerconaXtraDBCluster) (string, bool) {
	if pxc.Status.Status != pxcv1.AppStateError {
		return "", false
	}
	// The operator keeps the error it failed on after the messages its last
	// complete pass recorded about the pods.
	message := ""
	for _, m := range slices.Backward(pxc.Status.Messages) {
		if rest, ok := strings.CutPrefix(m, "Error: "); ok {
			message = strings.TrimSpace(rest)
			break
		}
	}
	if message == "" {
		message = strings.TrimSpace(strings.Join(pxc.Status.Messages, "; "))
	}
	if message == "" {
		message = "The operator reports an error without a message"
	}
	_, transient := transientWait(message)
	return message, !transient
}

// transientWait returns what the cluster waits for while the operator
// recovers from an error that is no failure, and whether message is one.
func transientWait(message string) (string, bool) {
	for _, known := range transientUpstreamErrors {
		if strings.Contains(message, known.fragment) {
			return known.wait, true
		}
	}
	return "", false
}

// upstreamErrorReason picks the reason for an operator error from its message.
func upstreamErrorReason(message string) string {
	for _, known := range upstreamErrorReasons {
		if strings.Contains(message, known.fragment) {
			return known.reason
		}
	}
	return corev1alpha1.ReasonEngineError
}

// waitingFor says what keeps the cluster from running what the Instance asks
// for, in the operator's own words where it has any. It returns "" when
// nothing does: the operator reports ready for the latest generation and
// every StatefulSet has rolled out.
func waitingFor(pxc *pxcv1.PerconaXtraDBCluster, sets []appsv1.StatefulSet, restore *restoreInProgress, pause pauseCause) string {
	state := pxc.Status.Status
	switch {
	case pause == pausedOutside:
		return "The cluster is paused on its PerconaXtraDBCluster object"
	case restore != nil:
		return restore.message
	case pause == pausedForTLSToggle:
		return fmt.Sprintf("Waiting for the operator to restart the cluster with TLS %s", wantedTLSState(pxc))
	case state == pxcv1.AppStateError:
		message, _ := upstreamError(pxc)
		if wait, transient := transientWait(message); transient {
			return wait
		}
		return "The operator reports an error: " + message
	case pxc.Status.ObservedGeneration == 0:
		return "Waiting for the operator to start creating the cluster"
	case pxc.Status.ObservedGeneration < pxc.Generation:
		return "Waiting for the operator to apply the latest configuration"
	case pxc.PVCResizeInProgress():
		return "Waiting for the volumes of the PXC nodes to be resized to " + requestedStorage(pxc)
	case state != pxcv1.AppStateReady:
		return notReadyWait(pxc, sets)
	}

	// The operator decides that nothing is rolling from the StatefulSets'
	// status. Right after it changes a pod template that status still
	// describes the previous one, so it can report ready for the latest
	// generation before the rolling restart has started.
	if wait := cmp.Or(rolloutWait(sets), podsWait(sets)); wait != "" {
		return "Waiting for " + wait
	}
	return ""
}

// clusterComponent is what the operator publishes about one of the cluster's
// StatefulSets.
type clusterComponent struct {
	label  string
	status pxcv1.AppStatus
}

// clusterComponents returns the PXC nodes and the proxy the cluster runs.
func clusterComponents(pxc *pxcv1.PerconaXtraDBCluster) []clusterComponent {
	components := []clusterComponent{{label: "PXC nodes", status: pxc.Status.PXC}}
	if pxc.Spec.HAProxyEnabled() {
		components = append(components, clusterComponent{label: "HAProxy", status: pxc.Status.HAProxy})
	}
	if pxc.Spec.ProxySQLEnabled() {
		components = append(components, clusterComponent{label: "ProxySQL", status: pxc.Status.ProxySQL})
	}
	return components
}

// notReadyWait says what a cluster the operator does not report ready is
// waiting for: what the operator says about the pods first, then a rolling
// update, then the pods the operator does not count as ready.
//
// A rolling update comes before the operator's counts because those leave
// out the nodes that still run the previous pod template, which serve all
// the same.
func notReadyWait(pxc *pxcv1.PerconaXtraDBCluster, sets []appsv1.StatefulSet) string {
	var messages, counts []string
	for _, comp := range clusterComponents(pxc) {
		st := comp.status
		switch {
		case st.Message != "":
			messages = append(messages, fmt.Sprintf("%s (%s)", comp.label, strings.TrimRight(st.Message, "; ")))
		case st.Ready < st.Size:
			counts = append(counts, fmt.Sprintf("%s (%d of %d ready)", comp.label, st.Ready, st.Size))
		}
	}
	if len(messages) > 0 {
		return "Waiting for " + strings.Join(messages, "; ")
	}
	if wait := rolloutWait(sets); wait != "" {
		return "Waiting for " + wait
	}
	if len(counts) > 0 {
		return "Waiting for " + strings.Join(counts, "; ")
	}
	if wait := podsWait(sets); wait != "" {
		return "Waiting for " + wait
	}
	state := pxc.Status.Status
	if state == "" {
		state = "nothing"
	}
	return fmt.Sprintf("Waiting for the operator to report the cluster ready (it reports %s)", state)
}

// rolloutWait returns what the first StatefulSet that has not rolled out its
// latest pod template is waiting for, or "" when all have.
//
// The operator replaces the PXC nodes itself, so their StatefulSet uses the
// OnDelete strategy. Its currentRevision then never moves, and a rollout is
// complete when the controller has seen the latest pod template and every
// pod runs it.
func rolloutWait(sets []appsv1.StatefulSet) string {
	for i := range sets {
		set := &sets[i]
		if !set.DeletionTimestamp.IsZero() {
			continue
		}
		st := set.Status
		switch {
		case st.ObservedGeneration < set.Generation:
			return fmt.Sprintf("StatefulSet %s to pick up its new pod template", set.Name)
		case st.UpdatedReplicas < st.Replicas:
			return fmt.Sprintf("StatefulSet %s to roll out (%d of %d pods updated)", set.Name, st.UpdatedReplicas, st.Replicas)
		}
	}
	return ""
}

// podsWait returns what the first StatefulSet that does not have all its
// pods ready is waiting for, or "" when all have them.
func podsWait(sets []appsv1.StatefulSet) string {
	for i := range sets {
		set := &sets[i]
		if !set.DeletionTimestamp.IsZero() {
			continue
		}
		want := int32(1)
		if set.Spec.Replicas != nil {
			want = *set.Spec.Replicas
		}
		st := set.Status
		switch {
		case st.Replicas != want:
			return fmt.Sprintf("StatefulSet %s to scale to %d pods (it has %d)", set.Name, want, st.Replicas)
		case st.ReadyReplicas < st.Replicas:
			return fmt.Sprintf("StatefulSet %s to become ready (%d of %d pods ready)", set.Name, st.ReadyReplicas, st.Replicas)
		}
	}
	return ""
}

// availability says whether clients can connect: they reach the database
// through the proxy, which needs a PXC node to send them to, so both need a
// ready pod. Without a proxy the PXC nodes alone count.
//
// It reads the ready counts the operator recorded on its last pass over the
// cluster, never the pods: a node waiting for a full crash recovery passes
// its readiness probe without serving, and the operator leaves it out. The
// operator counts only the nodes that run the latest pod template, so early
// in a rolling update, before the first node has restarted, it counts none
// although the others still serve; the answer is then unknown.
func availability(pxc *pxcv1.PerconaXtraDBCluster, sets []appsv1.StatefulSet, restore *restoreInProgress, pause pauseCause) (controller.Availability, string, string) {
	state := pxc.Status.Status
	switch {
	case restore != nil:
		return controller.Unavailable, reasonRestoring, "A restore is replacing the data"
	case pause == pausedForTLSToggle:
		return controller.Unavailable, reasonPaused, fmt.Sprintf("The operator paused the cluster to restart it with TLS %s", wantedTLSState(pxc))
	case pause != notPaused:
		return controller.Unavailable, reasonPaused, "The cluster is paused"
	case state == pxcv1.AppStatePaused, state == pxcv1.AppStateStopping:
		return controller.Unavailable, reasonPaused, "The cluster is starting again after a pause"
	case state == "":
		// The operator has not looked at the cluster yet. The runtime knows
		// whether it has ever been available.
		return controller.AvailabilityUnreported, "", ""
	}

	if pxc.Status.PXC.Ready < 1 {
		if set := statefulSet(sets, pxc.Name+"-pxc"); set != nil && set.Status.UpdatedReplicas < set.Status.Replicas {
			return controller.AvailabilityUnknown, reasonReadyNodesNotCounted,
				"The operator counts only the PXC nodes that run the latest pod template, and none does yet"
		}
		return controller.Unavailable, reasonNoNodeReady, "No PXC node is ready"
	}
	for _, comp := range clusterComponents(pxc)[1:] {
		if comp.status.Ready < 1 {
			return controller.Unavailable, reasonNoProxyReady, fmt.Sprintf("No %s pod is ready", comp.label)
		}
	}
	return controller.Available, "", ""
}

// statefulSet returns the StatefulSet with the name, or nil.
func statefulSet(sets []appsv1.StatefulSet, name string) *appsv1.StatefulSet {
	for i := range sets {
		if sets[i].Name == name {
			return &sets[i]
		}
	}
	return nil
}

// engineImage returns the image the PerconaXtraDBCluster asks the PXC nodes
// to run.
func engineImage(pxc *pxcv1.PerconaXtraDBCluster) string {
	if pxc.Spec.PXC == nil || pxc.Spec.PXC.PodSpec == nil {
		return ""
	}
	return pxc.Spec.PXC.Image
}

// requestedStorage returns the volume size the PerconaXtraDBCluster asks for
// the PXC nodes.
func requestedStorage(pxc *pxcv1.PerconaXtraDBCluster) string {
	if pxc.Spec.PXC == nil || pxc.Spec.PXC.PodSpec == nil || pxc.Spec.PXC.VolumeSpec == nil ||
		pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim == nil {
		return "the requested size"
	}
	size, ok := pxc.Spec.PXC.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	if !ok {
		return "the requested size"
	}
	return size.String()
}

// enqueueStatefulSetInstance maps a StatefulSet the operator runs for a
// cluster to the Instance of that cluster. Whether the Instance has converged
// depends on its StatefulSets having rolled out, and the operator's own
// status may not change again once they have. The StatefulSets carry no
// labels of the cluster, so they are mapped by their controller.
func enqueueStatefulSetInstance() func(ctx context.Context, obj client.Object) []reconcile.Request {
	return func(_ context.Context, obj client.Object) []reconcile.Request {
		owner := metav1.GetControllerOf(obj)
		if owner == nil || owner.Kind != "PerconaXtraDBCluster" {
			return nil
		}
		if gv, err := schema.ParseGroupVersion(owner.APIVersion); err != nil || gv.Group != pxcv1.SchemeGroupVersion.Group {
			return nil
		}
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: owner.Name},
		}}
	}
}
