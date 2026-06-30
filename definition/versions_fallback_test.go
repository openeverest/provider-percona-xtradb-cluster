package definition

import "testing"

func TestBackupImageForEngineVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		engineVersion string
		wantImage     string
	}{
		{
			name:          "bundle engine resolves bundled backup image",
			engineVersion: "8.4.8-8.1",
			wantImage:     "percona/percona-xtrabackup:8.4.0-5.1",
		},
		{
			name:          "unknown engine falls back to default backup image",
			engineVersion: "9.9.9-test",
			wantImage:     "percona/percona-xtrabackup:8.4.0-5.1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := BackupImageForEngineVersion(tt.engineVersion)
			if err != nil {
				t.Fatalf("BackupImageForEngineVersion() error = %v", err)
			}
			if got != tt.wantImage {
				t.Fatalf("BackupImageForEngineVersion() = %q, want %q", got, tt.wantImage)
			}
		})
	}
}
