// Package config loads the imgsrv YAML config file.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Quality holds encoder quality settings.
type Quality struct {
	// Default is the quality used for all sized derivatives.
	Default int `yaml:"default"`
	// Original is the quality used for the `original` token.
	Original int `yaml:"original"`
}

// Config is the mounted YAML config: allowlists and quality settings.
type Config struct {
	// Dimensions is the allowlist of permitted dimension numbers. Every
	// number appearing in a size token must be in this list.
	Dimensions []int   `yaml:"dimensions"`
	Quality    Quality `yaml:"quality"`
	// InputExtensions is the priority-ordered list of original file
	// extensions (without dot). When several originals share a basename,
	// the earliest extension in this list wins.
	InputExtensions []string `yaml:"input_extensions"`
}

// Default returns the built-in defaults applied before the file is read.
func Default() Config {
	return Config{
		Quality:         Quality{Default: 80, Original: 90},
		InputExtensions: []string{"jpg", "jpeg", "png", "tif", "tiff"},
	}
}

// Load reads and validates the YAML config at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (c Config) validate() error {
	if len(c.Dimensions) == 0 {
		return fmt.Errorf("dimensions list is empty")
	}
	for _, d := range c.Dimensions {
		if d <= 0 {
			return fmt.Errorf("invalid dimension %d", d)
		}
	}
	for name, q := range map[string]int{"default": c.Quality.Default, "original": c.Quality.Original} {
		if q < 1 || q > 100 {
			return fmt.Errorf("quality.%s must be 1-100, got %d", name, q)
		}
	}
	if len(c.InputExtensions) == 0 {
		return fmt.Errorf("input_extensions list is empty")
	}
	return nil
}
