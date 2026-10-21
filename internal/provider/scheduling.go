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
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
)

// applyScheduling places one component's pods. Unless the user brings their
// own affinity, the operator's required anti-affinity keeps the pods in
// separate antiAffinityKey domains; pxcv1.AffinityTopologyKeyOff disables it.
func applyScheduling(spec *pxcv1.PodSpec, policy *apicommon.SchedulingPolicy, podLabels map[string]string, antiAffinityKey string) {
	spec.Affinity = &pxcv1.PodAffinity{TopologyKey: new(antiAffinityKey)}
	if policy != nil && policy.Affinity != nil {
		spec.Affinity = &pxcv1.PodAffinity{Advanced: policy.Affinity}
	}
	spec.TopologySpreadConstraints = controller.TopologySpreadConstraints(policy, podLabels)
}

// labelPods labels every component's pods so the runtime reports on them in
// the Instance status. The operator adds them to the pod templates only, never
// to the StatefulSet selectors.
func labelPods(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) {
	pxc.Spec.PXC.Labels = c.PodLabels(common.ComponentEngine)
	if pxc.Spec.HAProxy != nil {
		pxc.Spec.HAProxy.Labels = c.PodLabels(common.ComponentProxy)
	}
	if pxc.Spec.ProxySQL != nil {
		pxc.Spec.ProxySQL.Labels = c.PodLabels(common.ComponentProxy)
	}
}
