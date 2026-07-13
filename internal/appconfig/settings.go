package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const settingsFile = "settings.json"

// Settings holds persistent nomadl defaults.
type Settings struct {
	IngestServices []string `json:"ingest_services"`
}

// Store reads and writes nomadl settings from disk.
type Store struct {
	path string
}

// DefaultDir returns the directory for nomadl configuration and state.
func DefaultDir() (string, error) {
	if xdgDir := os.Getenv("XDG_CONFIG_HOME"); xdgDir != "" {
		return filepath.Join(xdgDir, "nomadl"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	return filepath.Join(home, ".config", "nomadl"), nil
}

// NewStore returns a settings store rooted at configDir.
func NewStore(configDir string) Store {
	return Store{path: filepath.Join(configDir, settingsFile)}
}

// Path returns the settings file path.
func (s Store) Path() string {
	return s.path
}

// Load reads settings from disk, returning defaults when the file is absent.
func (s Store) Load() (Settings, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Settings{}, nil
		}
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}

	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	return settings, nil
}

// Save writes settings to disk.
func (s Store) Save(settings Settings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
