package migration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const BundleManifestFile = "wanix-migration.json"

// ReadBundleManifest reads wanix-migration.json from dir.
func ReadBundleManifest(dir string) (BundleManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, BundleManifestFile))
	if err != nil {
		return BundleManifest{}, fmt.Errorf("read bundle manifest: %w", err)
	}
	var manifest BundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BundleManifest{}, fmt.Errorf("parse bundle manifest: %w", err)
	}
	if err := ValidateBundleManifest(manifest); err != nil {
		return BundleManifest{}, err
	}
	return manifest, nil
}

// WriteBundleManifest writes manifest as wanix-migration.json in dir.
//
// It creates dir when needed and fails if the manifest file already exists.
func WriteBundleManifest(dir string, manifest BundleManifest) error {
	if err := ValidateBundleManifest(manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create bundle dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, BundleManifestFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create bundle manifest: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	err = enc.Encode(manifest)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write bundle manifest: %w", err)
	}
	return nil
}
