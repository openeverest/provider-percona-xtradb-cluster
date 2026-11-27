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

	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
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

// schedules gives the Instance one backup storage with the given schedules,
// all enabled.
func schedules(list ...corev1alpha1.InstanceBackupSchedule) func(*corev1alpha1.Instance) {
	return func(in *corev1alpha1.Instance) {
		for i := range list {
			list[i].Enabled = true
		}
		in.Spec.Backup = &corev1alpha1.InstanceBackupSpec{
			Enabled: true,
			Storages: []corev1alpha1.InstanceBackupStorage{{
				StorageRef: apicommon.ObjectRef{Name: "s3-a"},
				Schedules:  list,
			}},
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
		{
			name:   "schedule name is an arbitrary user-facing identifier",
			mutate: schedules(corev1alpha1.InstanceBackupSchedule{Name: "nightly-full", Cron: "0 1 * * *"}),
		},
		{
			name: "two schedules on the same storage are allowed",
			mutate: schedules(
				corev1alpha1.InstanceBackupSchedule{Name: "weekly", Cron: "0 1 * * 0"},
				corev1alpha1.InstanceBackupSchedule{Name: "monthly", Cron: "0 1 1 * *"},
			),
		},
		{
			name: "duplicate schedule name",
			mutate: schedules(
				corev1alpha1.InstanceBackupSchedule{Name: "nightly", Cron: "0 1 * * *"},
				corev1alpha1.InstanceBackupSchedule{Name: "nightly", Cron: "0 2 * * *"},
			),
			want: []string{"spec.backup.storages[0].schedules[1].name"},
		},
		// Whether a schedule's Parameters conform to the resolved
		// BackupClass's parametersSchema is checked by
		// validateBackupScheduleParameters, which needs a live BackupClass
		// read and so runs from ValidateReferences — see
		// TestValidateBackupScheduleParameters in backup_params_test.go, not
		// this admission-only (Context-less) harness.
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

// TestValidateReferences covers the checks ValidateReferences runs itself
// (it mostly delegates to validateCertificateSecretsAndChange in tls.go and
// validateBackupScheduleParameters in backup_params.go, each covered by its
// own tests): that it reaches both, and surfaces their errors together.
func TestValidateReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		parameters string
		objs       []client.Object
		wantErr    string
	}{
		{
			name:       "defaults ok",
			parameters: "",
		},
		{
			name:       "missing certificate secret",
			parameters: `{"tls":{"certificateSecretName":"missing"}}`,
			wantErr:    `Not found`,
		},
		{
			name:       "custom certificate ok",
			parameters: `{"tls":{"certificateSecretName":"my-tls"}}`,
			objs:       []client.Object{certSecret("my-tls")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tlsContext(t, tt.parameters, tt.objs...)
			errs, err := (&PXCProvider{}).ValidateReferences(c)
			require.NoError(t, err)
			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), tt.wantErr)
		})
	}
}

func TestValidateReferencesRejectsCertificateSourceChange(t *testing.T) {
	t.Parallel()

	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
		Spec:       pxcv1.PerconaXtraDBClusterSpec{SSLSecretName: "old-tls"},
	}
	c := tlsContext(t, `{"tls":{"certificateSecretName":"new-tls"}}`, existing, certSecret("new-tls"))

	errs, err := (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "certificates and issuer can't change")
}

func TestValidateReferencesAllowsModeChange(t *testing.T) {
	t.Parallel()

	existing := &pxcv1.PerconaXtraDBCluster{
		ObjectMeta: metav1.ObjectMeta{Name: tlsTestInstance, Namespace: tlsTestNamespace},
	}
	c := tlsContext(t, `{"tls":{"mode":"requireTLS"}}`, existing)

	errs, err := (&PXCProvider{}).ValidateReferences(c)
	require.NoError(t, err)
	assert.Empty(t, errs)
}
