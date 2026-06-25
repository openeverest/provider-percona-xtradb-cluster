// Package pxc contains the schema-bearing Go types for the
// "pxc" BackupClass. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// BackupClass manifest.
//
// +k8s:openapi-gen=true
package pxc

// PxcBackupConfig describes the configuration accepted by Backup CRs that
// target this class (spec.config). Add fields the user can set per backup.
type PxcBackupConfig struct{}

// PxcRestoreConfig describes the configuration accepted by Restore CRs that
// target this class (spec.config). Add fields the user can set per restore.
type PxcRestoreConfig struct{}

// PxcPITRConfig describes the per-storage PITR custom config exposed to
// Instance.spec.backup.storages[].pitr.config. Add fields a provider needs
// to fine-tune its PITR pipeline (oplog span, compression, retention, etc.).
type PxcPITRConfig struct {
	// TimeBetweenUploads controls binlog upload interval in seconds.
	TimeBetweenUploads *float64 `json:"timeBetweenUploads,omitempty"`
	// TimeoutSeconds controls timeout for each PITR upload in seconds.
	TimeoutSeconds *float64 `json:"timeoutSeconds,omitempty"`
}
