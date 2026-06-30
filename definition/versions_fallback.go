package definition

import (
	_ "embed"
	"fmt"
	"sync"

	"sigs.k8s.io/yaml"
)

type embeddedVersionSpec struct {
	ComponentTypes map[string]struct {
		Versions []struct {
			Version string `yaml:"version"`
			Image   string `yaml:"image"`
			Default bool   `yaml:"default"`
		} `yaml:"versions"`
	} `yaml:"componentTypes"`
	Versions []struct {
		Components map[string]string `yaml:"components"`
	} `yaml:"versions"`
}

//go:embed versions.yaml
var embeddedVersionsYAML []byte

var (
	embeddedVersionsOnce sync.Once
	embeddedVersions     embeddedVersionSpec
	embeddedVersionsErr  error
)

func loadEmbeddedVersions() (embeddedVersionSpec, error) {
	embeddedVersionsOnce.Do(func() {
		embeddedVersionsErr = yaml.Unmarshal(embeddedVersionsYAML, &embeddedVersions)
	})
	if embeddedVersionsErr != nil {
		return embeddedVersionSpec{}, fmt.Errorf("decode embedded versions.yaml: %w", embeddedVersionsErr)
	}
	return embeddedVersions, nil
}

// BackupImageForEngineVersion resolves the backup image from embedded definition
// data by matching the engine version to a bundle, then resolving that bundle's
// backup component image. If no matching bundle exists, it falls back to the
// default backup image from componentTypes.backup.
func BackupImageForEngineVersion(engineVersion string) (string, error) {
	spec, err := loadEmbeddedVersions()
	if err != nil {
		return "", err
	}

	backupVersion := ""
	for _, bundle := range spec.Versions {
		if bundle.Components["engine"] == engineVersion {
			backupVersion = bundle.Components["backup"]
			break
		}
	}

	backupVersions, ok := spec.ComponentTypes["backup"]
	if !ok {
		return "", nil
	}

	if backupVersion != "" {
		for _, v := range backupVersions.Versions {
			if v.Version == backupVersion {
				return v.Image, nil
			}
		}
	}

	for _, v := range backupVersions.Versions {
		if v.Default && v.Image != "" {
			return v.Image, nil
		}
	}
	for _, v := range backupVersions.Versions {
		if v.Image != "" {
			return v.Image, nil
		}
	}

	return "", nil
}
