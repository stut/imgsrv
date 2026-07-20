package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	cfg, err := Load(write(t, `
dimensions: [200, 400, 800, 1600, 600]
quality:
  default: 80
  original: 90
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Dimensions) != 5 || cfg.Dimensions[0] != 200 {
		t.Errorf("dimensions = %v", cfg.Dimensions)
	}
	if cfg.Quality.Default != 80 || cfg.Quality.Original != 90 {
		t.Errorf("quality = %+v", cfg.Quality)
	}
	// Defaults kick in for unset fields.
	if len(cfg.InputExtensions) == 0 || cfg.InputExtensions[0] != "jpg" {
		t.Errorf("input extensions = %v", cfg.InputExtensions)
	}
}

func TestLoadDefaultsQuality(t *testing.T) {
	cfg, err := Load(write(t, `dimensions: [400]`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Quality.Default != 80 || cfg.Quality.Original != 90 {
		t.Errorf("quality defaults = %+v", cfg.Quality)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"empty dimensions": `dimensions: []`,
		"no dimensions":    `quality: {default: 80}`,
		"bad dimension":    `dimensions: [400, -1]`,
		"quality too high": "dimensions: [400]\nquality: {default: 101}",
		"quality zero":     "dimensions: [400]\nquality: {original: 0}",
		"not yaml":         `{{{{`,
		"empty input exts": "dimensions: [400]\ninput_extensions: []",
	}
	for name, content := range cases {
		if _, err := Load(write(t, content)); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("want error for missing file")
	}
}
