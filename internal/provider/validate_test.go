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

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller/validationtest"

	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

const (
	providerSpecPath = "../../charts/provider-percona-xtradb-cluster/generated/provider-spec.yaml"
	clusterFixture   = "testdata/validate/cluster.yaml"
)

// component applies mutate to the named component of the Instance.
func component(name string, mutate func(*corev1alpha1.ComponentSpec)) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		c := in.Spec.Components[name]
		mutate(&c)
		in.Spec.Components[name] = c
	}
}

// proxyType sets the proxy component's type.
func proxyType(t string) func(*corev1alpha1.Instance) {
	return component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) { c.Type = t })
}

// pitr enables backups on storages, with PITR on each one given parameters.
func pitr(parameters ...string) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		in.Spec.Backup = &corev1alpha1.InstanceBackupSpec{Enabled: true}
		for i, raw := range parameters {
			s := corev1alpha1.InstanceBackupStorage{}
			s.StorageRef.Name = "s3-" + string(rune('a'+i))
			s.PITR = &corev1alpha1.InstanceBackupStoragePITR{Enabled: true}
			if raw != "" {
				s.PITR.Parameters = &runtime.RawExtension{Raw: []byte(raw)}
			}
			in.Spec.Backup.Storages = append(in.Spec.Backup.Storages, s)
		}
	}
}

// TestValidate runs the checks the runtime runs before every Sync: the
// schemas, supportedFields and catalog the provider publishes, then Validate.
func TestValidate(t *testing.T) {
	t.Parallel()
	v := validationtest.New(t, NewPXCProviderInterface(), providerSpecPath)

	tests := []struct {
		name   string
		mutate func(*corev1alpha1.Instance)
		want   []string
	}{
		{name: "valid cluster"},
		{name: "ProxySQL", mutate: proxyType(common.ProxyTypeProxySQL)},
		{
			name:   "proxy type outside the topology's choice",
			mutate: proxyType("router"),
			want:   []string{"spec.components[proxy].type"},
		},
		{
			name:   "engine type as the proxy",
			mutate: proxyType("pxc"),
			want:   []string{"spec.components[proxy].type"},
		},
		{
			name:   "proxy type unset",
			mutate: proxyType(""),
			want:   []string{"spec.components[proxy].type"},
		},
		{
			name:   "missing proxy",
			mutate: func(in *corev1alpha1.Instance) { delete(in.Spec.Components, common.ComponentProxy) },
			want:   []string{"spec.components[proxy]"},
		},
		{
			name: "missing proxy without a topology type",
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				delete(in.Spec.Components, common.ComponentProxy)
			},
			want: []string{"spec.components[proxy]"},
		},
		{
			name:   "zero engine replicas",
			mutate: component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(0)) }),
			want:   []string{"spec.components[engine].replicas"},
		},
		{
			name: "zero engine replicas without a topology type",
			mutate: func(in *corev1alpha1.Instance) {
				in.Spec.Topology = nil
				component(common.ComponentEngine, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(0)) })(in)
			},
			want: []string{"spec.components[engine].replicas"},
		},
		{
			name:   "zero proxy replicas",
			mutate: component(common.ComponentProxy, func(c *corev1alpha1.ComponentSpec) { c.Replicas = new(int32(0)) }),
			want:   []string{"spec.components[proxy].replicas"},
		},
		{name: "PITR on one storage", mutate: pitr(`{"timeBetweenUploads":60}`)},
		{
			name:   "PITR on two storages",
			mutate: pitr("", ""),
			want:   []string{"spec.backup.storages"},
		},
		{
			name:   "PITR uploads more often than once a second",
			mutate: pitr(`{"timeBetweenUploads":0}`),
			want:   []string{"spec.backup.storages[0].pitr.parameters"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := validationtest.Load(t, clusterFixture)
			if tt.mutate != nil {
				tt.mutate(in)
			}

			res := v.Run(in, nil)

			var got []string
			for _, err := range res.Errors {
				got = append(got, err.Field)
			}
			assert.Equal(t, tt.want, got, "errors: %v", res.Errors)
		})
	}
}
