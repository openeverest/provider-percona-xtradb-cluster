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

	backupv1alpha1 "github.com/openeverest/openeverest/v2/api/backup/v1alpha1"
	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Neither the core webhooks nor the runtime check Backup.spec.parameters,
// Restore.spec.parameters or InstanceBackupSchedule.parameters against the
// resolved BackupClass's schemas, so the provider does, using the same
// apicommon.ParametersSchema.Validate the runtime already uses for PITR
// parameters (see provider-runtime/internal/backupclass.PITRErrors). The
// schemas themselves come entirely from the kubebuilder markers on
// definition/backupclasses/pxc.PxcBackupConfig / PxcRestoreConfig — there is
// no hand-written field list to keep in sync.

// normalizeRawParameters treats an empty payload or a literal JSON "null" as
// absent, matching how the runtime's other generic parameters check
// (checkParameters in provider-runtime/validation/schema.go, used for
// Instance/component/topology parameters) treats a null value: no payload,
// not an empty object the schema must still satisfy. ParametersSchema.Validate
// itself has no such guard, since it decodes straight into map[string]any.
func normalizeRawParameters(raw *runtime.RawExtension) *runtime.RawExtension {
	if raw == nil {
		return nil
	}
	switch trimmed := bytes.TrimSpace(raw.Raw); {
	case len(trimmed) == 0, string(trimmed) == "null":
		return nil
	default:
		return raw
	}
}

// validateBackupParameters checks backup parameters (Backup.spec.parameters
// or InstanceBackupSchedule.parameters) against bc's parametersSchema
// (PxcBackupConfig, which currently declares no fields). A nil class (none
// resolved) rejects any non-empty payload, the same as an undeclared schema.
func validateBackupParameters(bc *backupv1alpha1.BackupClass, raw *runtime.RawExtension) error {
	var schema apicommon.ParametersSchema
	if bc != nil {
		schema = bc.Spec.ParametersSchema
	}
	return schema.Validate(normalizeRawParameters(raw))
}

// validateRestoreParameters is the Restore.spec.parameters counterpart,
// checked against bc's restoreParametersSchema (PxcRestoreConfig).
func validateRestoreParameters(bc *backupv1alpha1.BackupClass, raw *runtime.RawExtension) error {
	var schema apicommon.ParametersSchema
	if bc != nil {
		schema = bc.Spec.RestoreParametersSchema
	}
	return schema.Validate(normalizeRawParameters(raw))
}

// validateBackupScheduleNames rejects schedule names that repeat across all
// storages on the Instance: the PXC operator uses Name as the unique
// PXCScheduledBackupSchedule entry key (see applyBackupSettings in
// backup_mirror.go). This needs no BackupClass, so it runs from Validate
// (admission, no Context) rather than ValidateReferences.
func validateBackupScheduleNames(backup *corev1alpha1.InstanceBackupSpec) field.ErrorList {
	if backup == nil {
		return nil
	}
	var errs field.ErrorList
	storagesPath := field.NewPath("spec", "backup", "storages")
	seenNames := map[string]bool{}
	for i, storage := range backup.Storages {
		for j, schedule := range storage.Schedules {
			path := storagesPath.Index(i).Child("schedules").Index(j).Child("name")
			if seenNames[schedule.Name] {
				errs = append(errs, field.Duplicate(path, schedule.Name))
			}
			seenNames[schedule.Name] = true
		}
	}
	return errs
}

// validateBackupScheduleParameters checks every schedule's Parameters
// against the live BackupClass's parametersSchema. Resolving the class
// requires a Context (a BackupClass read), so this runs from
// ValidateReferences, not Validate.
func validateBackupScheduleParameters(c *controller.Context) (field.ErrorList, error) {
	backup := c.Instance().Spec.Backup
	if backup == nil {
		return nil, nil
	}
	bc, err := c.BackupClassForInstance()
	if err != nil {
		return nil, err
	}

	var errs field.ErrorList
	storagesPath := field.NewPath("spec", "backup", "storages")
	for i, storage := range backup.Storages {
		for j, schedule := range storage.Schedules {
			path := storagesPath.Index(i).Child("schedules").Index(j).Child("parameters")
			if err := validateBackupParameters(bc, schedule.Parameters); err != nil {
				errs = append(errs, field.Invalid(path, field.OmitValueType{}, err.Error()))
			}
		}
	}
	return errs, nil
}
