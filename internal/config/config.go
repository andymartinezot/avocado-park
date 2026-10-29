// Package config reads avocado-park's optional config.toml.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// PluginID is the herdr plugin id, also used for the config directory name.
const PluginID = "andymartinezot.avocado-park"

// Config holds user settings. Every field has a usable default.
type Config struct {
	// ParkIdleDays is the threshold for "park all idle" (panel P, park --idle).
	ParkIdleDays int `toml:"park_idle_days"`
	// AutoParkDays, when above zero, parks sessions idle this many days from
	// herdr's pane-focus hook, at most once an hour. Zero (the default) is off.
	AutoParkDays int `toml:"auto_park_days"`
	// RefreshSeconds is how often the panel refreshes.
	RefreshSeconds int `toml:"refresh_seconds"`
	// ClaudeDir is Claude Code's config dir (holds sessions/<pid>.json).
	ClaudeDir string `toml:"claude_dir"`
}

// Default returns the built-in settings.
func Default() Config {
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		home, _ := os.UserHomeDir()
		claude = filepath.Join(home, ".claude")
	}
	return Config{ParkIdleDays: 3, RefreshSeconds: 3, ClaudeDir: claude}
}

// Path is $HERDR_PLUGIN_CONFIG_DIR/config.toml, falling back to the directory
// herdr assigns this plugin so that the standalone CLI reads the same file.
func Path() string {
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "herdr", "plugins", "config", PluginID)
	}
	return filepath.Join(dir, "config.toml")
}

// Load reads the config file over the defaults. A missing file is not an error.
func Load() (Config, error) {
	c := Default()
	_, err := toml.DecodeFile(Path(), &c)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if c.ParkIdleDays < 1 {
		c.ParkIdleDays = Default().ParkIdleDays
	}
	if c.RefreshSeconds < 1 {
		c.RefreshSeconds = Default().RefreshSeconds
	}
	return c, err
}
