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
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// pmmTokenKey is the key of the PMM server token in the destination's
	// credentials Secret.
	pmmTokenKey = "token"
	// pmmServerTokenKey is where the operator reads the PMM 3 server token
	// in the users Secret.
	pmmServerTokenKey = "pmmservertoken"
)

// PMM client requests per engine size, from
// https://github.com/Tusamarco/mysqloperatorcalculator.
var (
	pmmRequestsSmall = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("95m"),
		corev1.ResourceMemory: resource.MustParse("99604Ki"),
	}
	pmmRequestsMedium = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("228m"),
		corev1.ResourceMemory: resource.MustParse("199168Ki"),
	}
	pmmRequestsLarge = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("228m"),
		corev1.ResourceMemory: resource.MustParse("796907Ki"),
	}
)

// pmmResources returns the PMM client resources: requests sized from the
// engine's memory, overridden by whatever the entry sets. A sized request
// above a limit the entry sets is lowered to that limit.
func pmmResources(engine, entry *corev1.ResourceRequirements) (corev1.ResourceRequirements, error) {
	requests := pmmRequestsSmall
	switch mem := memoryLimitOrRequest(engine); {
	case mem.Cmp(pxcMemLarge) >= 0:
		requests = pmmRequestsLarge
	case mem.Cmp(pxcMemMedium) >= 0:
		requests = pmmRequestsMedium
	}

	out := corev1.ResourceRequirements{Requests: maps.Clone(requests)}
	if entry == nil {
		return out, nil
	}
	for _, name := range slices.Sorted(maps.Keys(entry.Limits)) {
		limit := entry.Limits[name]
		if request, set := entry.Requests[name]; set {
			if request.Cmp(limit) > 0 {
				return corev1.ResourceRequirements{}, fmt.Errorf("resources.requests.%s (%s) is above resources.limits.%s (%s)",
					name, request.String(), name, limit.String())
			}
			continue
		}
		if sized, ok := out.Requests[name]; ok && sized.Cmp(limit) > 0 {
			out.Requests[name] = limit
		}
	}
	maps.Copy(out.Requests, entry.Requests)
	out.Limits = maps.Clone(entry.Limits)
	return out, nil
}

// pmmAdminParams returns the pmm-admin flags that label the instance's metrics
// in PMM, so consumers can scope queries to one instance. Label names follow
// the Prometheus form of the pod labels (core_openeverest_io_instance), and
// namespace matches the label on Kubernetes metrics in PMM. The operator runs
// pmm-admin through a shell; Kubernetes names need no quoting.
func pmmAdminParams(c *controller.Context, component string) string {
	labels := []string{
		"namespace=" + c.Namespace(),
		prometheusLabelName(controller.InstanceLabel) + "=" + c.Name(),
	}
	if component != "" {
		labels = append(labels, prometheusLabelName(controller.ComponentLabel)+"="+component)
	}
	return "--custom-labels=" + strings.Join(labels, ",")
}

var invalidPrometheusLabelChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// prometheusLabelName converts a Kubernetes label key to a valid Prometheus
// label name the way Prometheus does for Kubernetes service discovery.
func prometheusLabelName(key string) string {
	return invalidPrometheusLabelChars.ReplaceAllString(key, "_")
}

func pmmServerHostFromURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", errors.New("url is empty")
	}
	if !strings.Contains(rawURL, "://") {
		return strings.TrimSuffix(rawURL, "/"), nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", errors.New("missing host")
	}
	return u.Host, nil
}

// syncPMMServerToken mirrors token into the users Secret, where the operator
// reads it, or removes the mirror when token is nil. The operator creates the
// Secret; until it exists there is nothing to update.
func syncPMMServerToken(c *controller.Context, usersSecretName string, token []byte) error {
	secret := &corev1.Secret{}
	if err := c.Get(secret, usersSecretName); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return nil
		}
		return fmt.Errorf("get users Secret %q: %w", usersSecretName, err)
	}

	current, mirrored := secret.Data[pmmServerTokenKey]
	if (token == nil && !mirrored) || (token != nil && bytes.Equal(current, token)) {
		return nil
	}

	patch := client.MergeFrom(secret.DeepCopy())
	if token == nil {
		delete(secret.Data, pmmServerTokenKey)
	} else {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[pmmServerTokenKey] = token
	}
	if err := c.Client().Patch(c.Context(), secret, patch); err != nil {
		return fmt.Errorf("sync PMM server token to Secret %q: %w", usersSecretName, err)
	}
	return nil
}
