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
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"

	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

// Validate checks the rules about the Instance alone. The runtime runs it once
// the Instance matches the topologies, components, versions and parameter
// schemas the Provider declares.
func (p *PXCProvider) Validate(in controller.ValidationInput) controller.ValidationResult {
	instance := in.Instance
	componentsPath := field.NewPath("spec", "components")

	var errs field.ErrorList
	enginePath := componentsPath.Key(common.ComponentEngine)
	if engine, ok := instance.Spec.Components[common.ComponentEngine]; !ok {
		errs = append(errs, field.Required(enginePath, ""))
	} else if engine.Replicas != nil && *engine.Replicas < 1 {
		errs = append(errs, field.Invalid(enginePath.Child("replicas"), *engine.Replicas, "must be at least 1"))
	}

	proxyPath := componentsPath.Key(common.ComponentProxy)
	if proxy, ok := instance.Spec.Components[common.ComponentProxy]; !ok {
		errs = append(errs, field.Required(proxyPath, ""))
	} else {
		switch proxy.Type {
		case common.ProxyTypeHAProxy, common.ProxyTypeProxySQL:
		case "":
			errs = append(errs, field.Required(proxyPath.Child("type"), ""))
		default:
			errs = append(errs, field.NotSupported(proxyPath.Child("type"), proxy.Type,
				[]string{common.ProxyTypeHAProxy, common.ProxyTypeProxySQL},
			))
		}
		if proxy.Replicas != nil && *proxy.Replicas < 1 {
			errs = append(errs, field.Invalid(proxyPath.Child("replicas"), *proxy.Replicas, "must be at least 1"))
		}
	}

	errs = append(errs, validatePITR(instance.Spec.Backup)...)

	tls, err := tlsParameters(instance)
	if err != nil {
		errs = append(errs, field.Invalid(
			field.NewPath("spec", "parameters", "tls"), field.OmitValueType{}, err.Error(),
		))
	} else {
		errs = append(errs, validateTLSParameters(tls, field.NewPath("spec", "parameters", "tls"))...)
	}

	return controller.ValidationResult{Errors: errs}
}

// validatePITR checks the PITR parameters of each storage, and that at most
// one storage enables PITR.
func validatePITR(backup *corev1alpha1.InstanceBackupSpec) field.ErrorList {
	if backup == nil || !backup.Enabled {
		return nil
	}

	path := field.NewPath("spec", "backup", "storages")
	var errs field.ErrorList
	var enabled []string
	for i, s := range backup.Storages {
		if s.PITR == nil || !s.PITR.Enabled {
			continue
		}
		enabled = append(enabled, s.StorageRef.Name)

		var raw []byte
		if s.PITR.Parameters != nil {
			raw = s.PITR.Parameters.Raw
		}
		if _, err := decodeAndValidatePITRConfig(s.StorageRef.Name, raw); err != nil {
			errs = append(errs, field.Invalid(path.Index(i).Child("pitr", "parameters"), field.OmitValueType{}, err.Error()))
		}
	}
	if len(enabled) > 1 {
		errs = append(errs, field.Invalid(path, field.OmitValueType{}, fmt.Sprintf(
			"PXC supports PITR on at most one storage, got %d: %s", len(enabled), strings.Join(enabled, ", "),
		)))
	}

	return errs
}
