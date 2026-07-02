package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type pitrValueDefaults struct {
	Default float64 `yaml:"default"`
	Min     float64 `yaml:"min"`
}

type defaultsFile struct {
	PITR struct {
		TimeBetweenUploads pitrValueDefaults `yaml:"timeBetweenUploads"`
		TimeoutSeconds     pitrValueDefaults `yaml:"timeoutSeconds"`
	} `yaml:"pitr"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "pitr-defaults-gen: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	repoRoot := flag.String("repo-root", ".", "repository root path")
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repo root: %w", err)
	}

	defaultsPath := filepath.Join(root, "definition", "backupclasses", "pxc", "defaults.yaml")
	defaults, err := loadDefaults(defaultsPath)
	if err != nil {
		return err
	}

	if err := applyUpdate(filepath.Join(root, "internal", "provider", "backup.go"), func(in string) (string, error) {
		return updateBackendConstants(in, defaults)
	}); err != nil {
		return err
	}

	if err := applyUpdate(filepath.Join(root, "definition", "backupclasses", "pxc", "types.go"), func(in string) (string, error) {
		return updateTypeMarkers(in, defaults)
	}); err != nil {
		return err
	}

	if err := applyUpdate(filepath.Join(root, "definition", "backupclasses", "pxc", "ui.yaml"), func(in string) (string, error) {
		return updateUIDefaults(in, defaults)
	}); err != nil {
		return err
	}

	return nil
}

func loadDefaults(path string) (defaultsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return defaultsFile{}, fmt.Errorf("read %s: %w", path, err)
	}

	var out defaultsFile
	if err := yaml.Unmarshal(b, &out); err != nil {
		return defaultsFile{}, fmt.Errorf("decode %s: %w", path, err)
	}

	for name, v := range map[string]pitrValueDefaults{
		"timeBetweenUploads": out.PITR.TimeBetweenUploads,
		"timeoutSeconds":     out.PITR.TimeoutSeconds,
	} {
		if v.Min <= 0 {
			return defaultsFile{}, fmt.Errorf("%s.min must be > 0", name)
		}
		if v.Default < v.Min {
			return defaultsFile{}, fmt.Errorf("%s.default must be >= min", name)
		}
	}

	return out, nil
}

func applyUpdate(path string, update func(string) (string, error)) error {
	in, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	out, err := update(string(in))
	if err != nil {
		return fmt.Errorf("update %s: %w", path, err)
	}

	if out == string(in) {
		return nil
	}

	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}

func updateBackendConstants(in string, defs defaultsFile) (string, error) {
	out, err := replaceOne(
		in,
		regexp.MustCompile(`(?m)^(\s*defaultPITRTimeBetweenUploads\s*=\s*)([^\s]+)(\s*)$`),
		"defaultPITRTimeBetweenUploads",
		`${1}`+formatNumber(defs.PITR.TimeBetweenUploads.Default)+`${3}`,
	)
	if err != nil {
		return "", err
	}

	out, err = replaceOne(
		out,
		regexp.MustCompile(`(?m)^(\s*defaultPITRTimeoutSeconds\s*=\s*)([^\s]+)(\s*)$`),
		"defaultPITRTimeoutSeconds",
		`${1}`+formatNumber(defs.PITR.TimeoutSeconds.Default)+`${3}`,
	)
	if err != nil {
		return "", err
	}

	return out, nil
}

func updateTypeMarkers(in string, defs defaultsFile) (string, error) {
	out, err := replaceFieldMarkers(in, "TimeBetweenUploads", defs.PITR.TimeBetweenUploads)
	if err != nil {
		return "", err
	}

	out, err = replaceFieldMarkers(out, "TimeoutSeconds", defs.PITR.TimeoutSeconds)
	if err != nil {
		return "", err
	}

	return out, nil
}

func replaceFieldMarkers(in, fieldName string, defs pitrValueDefaults) (string, error) {
	pattern := regexp.MustCompile(
		`(?s)(// ` + regexp.QuoteMeta(fieldName) + `[^\n]*\n\s*// \+k8s:minimum=)([^\n]+)(\n\s*// \+default=)([^\n]+)(\n\s*` + regexp.QuoteMeta(fieldName) + `\s+\*float64)`)

	return replaceOne(
		in,
		pattern,
		fieldName+" markers",
		`${1}`+formatNumber(defs.Min)+`${3}`+formatNumber(defs.Default)+`${5}`,
	)
}

func updateUIDefaults(in string, defs defaultsFile) (string, error) {
	out, err := replaceOne(
		in,
		regexp.MustCompile(`(?s)(timeBetweenUploads:\n\s+path: spec\.backup\.storages\[\]\.pitr\.config\.timeBetweenUploads\n\s+uiType: number\n\s+fieldParams:\n\s+label: Upload interval \(seconds\)\n\s+defaultValue: )([^\n]+)(\n\s+validation:\n\s+min: )([^\n]+)`),
		"timeBetweenUploads ui values",
		`${1}`+formatNumber(defs.PITR.TimeBetweenUploads.Default)+`${3}`+formatNumber(defs.PITR.TimeBetweenUploads.Min),
	)
	if err != nil {
		return "", err
	}

	out, err = replaceOne(
		out,
		regexp.MustCompile(`(?s)(timeoutSeconds:\n\s+path: spec\.backup\.storages\[\]\.pitr\.config\.timeoutSeconds\n\s+uiType: number\n\s+fieldParams:\n\s+label: Upload timeout \(seconds\)\n\s+defaultValue: )([^\n]+)(\n\s+validation:\n\s+min: )([^\n]+)`),
		"timeoutSeconds ui values",
		`${1}`+formatNumber(defs.PITR.TimeoutSeconds.Default)+`${3}`+formatNumber(defs.PITR.TimeoutSeconds.Min),
	)
	if err != nil {
		return "", err
	}

	return out, nil
}

func replaceOne(in string, re *regexp.Regexp, label, replacement string) (string, error) {
	matches := re.FindAllStringSubmatchIndex(in, -1)
	if len(matches) != 1 {
		return "", fmt.Errorf("expected one match for %s, got %d", label, len(matches))
	}

	return re.ReplaceAllString(in, replacement), nil
}

func formatNumber(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 6, 64), "0"), ".")
}
