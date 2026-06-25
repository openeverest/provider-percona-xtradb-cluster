// Package openeverestperconapxcoperator contains the schema-bearing Go types for the
// "openeverest-percona-pxc-operator" BackupClass. Each struct here is converted to an OpenAPI
// v3 schema by `provider-sdk generate` and inlined into the generated
// BackupClass manifest.
//
// +k8s:openapi-gen=true
package openeverestperconapxcoperator

// OpeneverestPerconaPxcOperatorBackupConfig describes the configuration accepted by Backup CRs that
// target this class (spec.config). Add fields the user can set per backup.
type OpeneverestPerconaPxcOperatorBackupConfig struct{}

// OpeneverestPerconaPxcOperatorRestoreConfig describes the configuration accepted by Restore CRs that
// target this class (spec.config). Add fields the user can set per restore.
type OpeneverestPerconaPxcOperatorRestoreConfig struct{}

// OpeneverestPerconaPxcOperatorPITRConfig describes the per-storage PITR custom config exposed to
// Instance.spec.backup.storages[].pitr.config. Add fields a provider needs
// to fine-tune its PITR pipeline (oplog span, compression, retention, etc.).
type OpeneverestPerconaPxcOperatorPITRConfig struct{}
