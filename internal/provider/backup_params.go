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
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"

	pxcclass "github.com/openeverest/provider-percona-xtradb-cluster/definition/backupclasses/pxc"
)

// Neither the core webhooks nor the runtime check Backup.spec.parameters,
// Restore.spec.parameters or InstanceBackupSchedule.parameters against the
// pxc BackupClass's schemas, so the provider does.

// validateBackupParameters checks backup parameters (Backup.spec.parameters
// or InstanceBackupSchedule.parameters) against the pxc class's
// parametersSchema (PxcBackupConfig), which currently declares no fields.
func validateBackupParameters(raw *runtime.RawExtension) error {
	return decodeStrict(raw, &pxcclass.PxcBackupConfig{})
}

// validateRestoreParameters checks restore parameters against the pxc
// class's restoreParametersSchema (PxcRestoreConfig), which declares no
// fields.
func validateRestoreParameters(raw *runtime.RawExtension) error {
	return decodeStrict(raw, &pxcclass.PxcRestoreConfig{})
}

// decodeStrict decodes a parameters payload into target, rejecting fields
// target does not declare. An absent or null payload leaves target as is.
func decodeStrict(raw *runtime.RawExtension, target any) error {
	if raw == nil || len(bytes.TrimSpace(raw.Raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw.Raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("invalid parameters: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid parameters: unexpected data after the JSON object")
	}
	return nil
}

// validateBackupSchedules checks the schedules of each backup storage: each
// schedule's Name must be unique across the whole Instance (the PXC operator
// uses it as the PXCScheduledBackupSchedule entry's name — see
// applyBackupSettings in backup_mirror.go), and its Parameters, when set,
// must decode cleanly against the pxc class's schema.
func validateBackupSchedules(backup *corev1alpha1.InstanceBackupSpec) field.ErrorList {
	if backup == nil {
		return nil
	}
	var errs field.ErrorList
	storagesPath := field.NewPath("spec", "backup", "storages")
	seenNames := map[string]bool{}
	for i, storage := range backup.Storages {
		for j, schedule := range storage.Schedules {
			path := storagesPath.Index(i).Child("schedules").Index(j)

			if seenNames[schedule.Name] {
				errs = append(errs, field.Duplicate(path.Child("name"), schedule.Name))
			}
			seenNames[schedule.Name] = true

			if err := validateBackupParameters(schedule.Parameters); err != nil {
				errs = append(errs, field.Invalid(path.Child("parameters"), field.OmitValueType{}, err.Error()))
			}
		}
	}
	return errs
}
