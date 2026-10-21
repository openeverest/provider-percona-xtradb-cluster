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

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPMMAdminParamsLabelTheInstance(t *testing.T) {
	t.Parallel()

	in := &corev1alpha1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "my-pxc", Namespace: "db"}}
	c := controller.NewContext(context.Background(), nil, in, "pxc")

	assert.Equal(t,
		"--custom-labels=namespace=db,core_openeverest_io_instance=my-pxc",
		pmmAdminParams(c, ""))
	assert.Equal(t,
		"--custom-labels=namespace=db,core_openeverest_io_instance=my-pxc,core_openeverest_io_component=proxy",
		pmmAdminParams(c, "proxy"))
}
