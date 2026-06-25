// Package pxcbackup contains the schema-bearing Go types for the
// "pxcbackup" BackupClass. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// BackupClass manifest.
//
// +k8s:openapi-gen=true
package pxcbackup

// PxcbackupBackupConfig describes the configuration accepted by Backup CRs that
// target this class (spec.config). Add fields the user can set per backup.
type PxcbackupBackupConfig struct{}

// PxcbackupRestoreConfig describes the configuration accepted by Restore CRs that
// target this class (spec.config). Add fields the user can set per restore.
type PxcbackupRestoreConfig struct{}

// PxcbackupPITRConfig describes the per-storage PITR custom config exposed to
// Instance.spec.backup.storages[].pitr.config. Add fields a provider needs
// to fine-tune its PITR pipeline (oplog span, compression, retention, etc.).
type PxcbackupPITRConfig struct{}
