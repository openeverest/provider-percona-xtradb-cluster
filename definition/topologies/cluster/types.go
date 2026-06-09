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

// Package cluster contains custom spec types for the PXC cluster topology.
//
// +k8s:openapi-gen=true
package cluster

// ProxyType identifies which proxy implementation to use for the cluster.
// +kubebuilder:validation:Enum=haproxy;proxysql
type ProxyType string

// ClusterTopologyConfig defines topology-specific configuration.
type ClusterTopologyConfig struct {
	// ProxyType selects which proxy is enabled for the cluster.
	// +kubebuilder:default=haproxy
	ProxyType ProxyType `json:"proxyType,omitempty"`
	// ProxyReplicas sets the number of replicas for the selected proxy.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	ProxyReplicas *int32 `json:"proxyReplicas,omitempty"`
}
