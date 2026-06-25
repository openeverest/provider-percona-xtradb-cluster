// Package pxcxtrabackup contains schema-bearing Go types for the
// PXC XtraBackup BackupClass.
//
// +k8s:openapi-gen=true
package pxcxtrabackup

// PXCXtraBackupBackupConfig describes optional backup request config.
type PXCXtraBackupBackupConfig struct{}

// PXCXtraBackupRestoreConfig describes optional restore request config.
type PXCXtraBackupRestoreConfig struct{}

// PXCXtraBackupPITRConfig configures PITR behavior per storage.
type PXCXtraBackupPITRConfig struct {
	// TimeBetweenUploads controls binlog upload interval in seconds.
	TimeBetweenUploads *float64 `json:"timeBetweenUploads,omitempty"`
	// TimeoutSeconds controls timeout for each PITR upload in seconds.
	TimeoutSeconds *float64 `json:"timeoutSeconds,omitempty"`
}
