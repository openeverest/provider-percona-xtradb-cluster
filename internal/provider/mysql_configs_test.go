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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDefaultConfigurationForEngine(t *testing.T) {
	t.Parallel()

	mem := func(q string) *corev1.ResourceRequirements {
		return &corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse(q),
			},
		}
	}

	assert.Equal(t, pxcConfigSizeTiny, defaultConfigurationForEngine(1, mem("512Mi")))
	assert.Equal(t, pxcConfigSizeTiny, defaultConfigurationForEngine(1, mem("1Gi")))
	assert.Equal(t, pxcConfigSizeSmall, defaultConfigurationForEngine(1, mem("2Gi")))
	assert.Equal(t, pxcConfigSizeSmall, defaultConfigurationForEngine(3, mem("4G")))
	assert.Equal(t, pxcConfigSizeMedium, defaultConfigurationForEngine(3, mem("8Gi")))
	assert.Equal(t, pxcConfigSizeLarge, defaultConfigurationForEngine(5, mem("32Gi")))
	assert.Equal(t, pxcConfigSizeTiny, defaultConfigurationForEngine(1, nil))
}
