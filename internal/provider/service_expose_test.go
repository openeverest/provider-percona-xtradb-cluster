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
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func service(t corev1.ServiceType) *corev1alpha1.Service {
	return &corev1alpha1.Service{ServiceType: t}
}

// TestApplyServiceExpose verifies that engine.Service and proxy.Service are
// applied independently: engine.Service always controls pxc.Spec.PXC.Expose,
// and proxy.Service always controls the enabled proxy's exposed endpoint.
// Neither falls back to or is overridden by the other.
func TestApplyServiceExpose(t *testing.T) {
	t.Parallel()

	haProxyCluster := func() *pxcv1.PerconaXtraDBCluster {
		return &pxcv1.PerconaXtraDBCluster{
			Spec: pxcv1.PerconaXtraDBClusterSpec{
				PXC:     &pxcv1.PXCSpec{},
				HAProxy: &pxcv1.HAProxySpec{PodSpec: pxcv1.PodSpec{Enabled: true}},
			},
		}
	}
	proxySQLCluster := func() *pxcv1.PerconaXtraDBCluster {
		return &pxcv1.PerconaXtraDBCluster{
			Spec: pxcv1.PerconaXtraDBClusterSpec{
				PXC:      &pxcv1.PXCSpec{},
				ProxySQL: &pxcv1.ProxySQLSpec{PodSpec: pxcv1.PodSpec{Enabled: true}},
			},
		}
	}

	tests := []struct {
		name       string
		engine     *corev1alpha1.Service
		proxy      *corev1alpha1.Service
		wantEngine pxcv1.ServiceExpose
		wantProxy  pxcv1.ServiceExpose
	}{
		{
			name: "no service leaves the operator defaults",
		},
		{
			name:       "engine ClusterIP, no proxy service",
			engine:     service(corev1.ServiceTypeClusterIP),
			wantEngine: pxcv1.ServiceExpose{Type: corev1.ServiceTypeClusterIP},
		},
		{
			name:      "proxy NodePort, no engine service",
			proxy:     service(corev1.ServiceTypeNodePort),
			wantProxy: pxcv1.ServiceExpose{Type: corev1.ServiceTypeNodePort},
		},
		{
			name:       "engine and proxy exposure are independent",
			engine:     service(corev1.ServiceTypeLoadBalancer),
			proxy:      service(corev1.ServiceTypeClusterIP),
			wantEngine: pxcv1.ServiceExpose{Type: corev1.ServiceTypeLoadBalancer},
			wantProxy:  pxcv1.ServiceExpose{Type: corev1.ServiceTypeClusterIP},
		},
		{
			name: "engine LoadBalancer with annotations and source ranges",
			engine: &corev1alpha1.Service{
				ServiceType: corev1.ServiceTypeLoadBalancer,
				Annotations: map[string]string{"a": "b"},
				LoadBalancerService: &corev1alpha1.LoadBalancerService{
					SourceRanges: corev1alpha1.SourceRanges{"10.0.0.0/8"},
				},
			},
			wantEngine: pxcv1.ServiceExpose{
				Type:                     corev1.ServiceTypeLoadBalancer,
				Annotations:              map[string]string{"a": "b"},
				LoadBalancerSourceRanges: []string{"10.0.0.0/8"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" (haproxy)", func(t *testing.T) {
			t.Parallel()
			pxc := haProxyCluster()
			applyServiceExpose(pxc, corev1alpha1.ComponentSpec{Service: tt.engine}, corev1alpha1.ComponentSpec{Service: tt.proxy})
			assert.Equal(t, tt.wantEngine, pxc.Spec.PXC.Expose)
			assert.Equal(t, tt.wantProxy, pxc.Spec.HAProxy.ExposePrimary)
		})
		t.Run(tt.name+" (proxysql)", func(t *testing.T) {
			t.Parallel()
			pxc := proxySQLCluster()
			applyServiceExpose(pxc, corev1alpha1.ComponentSpec{Service: tt.engine}, corev1alpha1.ComponentSpec{Service: tt.proxy})
			assert.Equal(t, tt.wantEngine, pxc.Spec.PXC.Expose)
			assert.Equal(t, tt.wantProxy, pxc.Spec.ProxySQL.Expose)
		})
	}
}
