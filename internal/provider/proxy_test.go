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
	"testing"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
)

func proxyWithConfiguration(t *testing.T, configuration string) *runtime.RawExtension {
	t.Helper()

	raw, err := json.Marshal(map[string]any{"configuration": configuration})
	require.NoError(t, err)
	return &runtime.RawExtension{Raw: raw}
}

func TestProxyConfigurationFromComponent(t *testing.T) {
	t.Parallel()

	t.Run("no parameters returns empty configuration", func(t *testing.T) {
		t.Parallel()
		cfg, err := proxyConfigurationFromComponent(corev1alpha1.ComponentSpec{})
		require.NoError(t, err)
		assert.Empty(t, cfg)
	})

	t.Run("configuration is passed through verbatim", func(t *testing.T) {
		t.Parallel()
		proxy := corev1alpha1.ComponentSpec{
			Type:       "haproxy",
			Parameters: proxyWithConfiguration(t, "timeout client 30s\n"),
		}
		cfg, err := proxyConfigurationFromComponent(proxy)
		require.NoError(t, err)
		assert.Equal(t, "timeout client 30s\n", cfg)
	})

	t.Run("invalid parameters return an error", func(t *testing.T) {
		t.Parallel()
		proxy := corev1alpha1.ComponentSpec{
			Type:       "haproxy",
			Parameters: &runtime.RawExtension{Raw: []byte("not-json")},
		}
		_, err := proxyConfigurationFromComponent(proxy)
		require.Error(t, err)
	})
}
