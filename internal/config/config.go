package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"vps-fleet-tui/internal/vps"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Servers []vps.VPS `yaml:"servers"`
}

func GetConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "vps-fleet", "config.yaml")
}

func LoadConfig() (*Config, error) {
	path := GetConfigPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{Servers: []vps.VPS{}}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	err = yaml.Unmarshal(data, &cfg)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}

// DefaultProfile is the name of the server list in config.yaml.
const DefaultProfile = "default"

// ProfilesDir holds additional server lists, one <name>.yaml per profile,
// in the same format as config.yaml.
func ProfilesDir() string {
	return filepath.Join(filepath.Dir(GetConfigPath()), "profiles")
}

// ListProfiles returns "default" followed by the profiles in ProfilesDir, sorted.
func ListProfiles() []string {
	names := []string{DefaultProfile}
	entries, _ := os.ReadDir(ProfilesDir())
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".yaml"); ok && !e.IsDir() && name != DefaultProfile {
			names = append(names, name)
		}
	}
	slices.Sort(names[1:])
	return names
}

// LoadProfile loads a profile's server list; "" or "default" is config.yaml.
func LoadProfile(name string) (*Config, error) {
	if name == "" || name == DefaultProfile {
		return LoadConfig()
	}
	data, err := os.ReadFile(filepath.Join(ProfilesDir(), name+".yaml"))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("profile %q not found (available: %s)", name, strings.Join(ListProfiles(), ", "))
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("profile %s: %w", name, err)
	}
	return &cfg, nil
}

// Settings are the user preferences edited in the settings screen. They live in
// their own file so saving them never rewrites the server list (and its passwords).
type Settings struct {
	BarColors   string `yaml:"bar_colors"`   // "current", "greyscale" or "custom"
	CustomStart string `yaml:"custom_start"` // hex colours used with "custom"
	CustomEnd   string `yaml:"custom_end"`
	KillSignal  string `yaml:"kill_signal"` // e.g. "TERM"
	SortOrder   string `yaml:"sort_order"`  // e.g. "config", "name", "cpu"
}

// Built-in bar colour schemes: start and end of the gradient.
var BarSchemes = map[string][2]string{
	"current":   {"#5A56E0", "#EE6FF8"},
	"greyscale": {"#4E4E4E", "#D0D0D0"},
}

func DefaultSettings() Settings {
	return Settings{
		BarColors:   "current",
		CustomStart: "#5A56E0",
		CustomEnd:   "#EE6FF8",
		KillSignal:  "TERM",
		SortOrder:   "config",
	}
}

// Gradient returns the start and end colour for the chosen scheme.
func (s Settings) Gradient() (string, string) {
	if c, ok := BarSchemes[s.BarColors]; ok {
		return c[0], c[1]
	}
	return s.CustomStart, s.CustomEnd
}

// SettingsPath is settings.yaml next to config.yaml.
func SettingsPath() string {
	return filepath.Join(filepath.Dir(GetConfigPath()), "settings.yaml")
}

// LoadSettings reads settings.yaml; missing file or fields fall back to the defaults.
func LoadSettings() (Settings, error) {
	s := DefaultSettings()
	data, err := os.ReadFile(SettingsPath())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = yaml.Unmarshal(data, &s)
	return s, err
}

func SaveSettings(s Settings) error {
	if err := os.MkdirAll(filepath.Dir(SettingsPath()), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(SettingsPath(), data, 0600); err != nil {
		return err
	}
	return os.Chmod(SettingsPath(), 0600) // WriteFile keeps the mode of an existing file
}

func SaveConfig(cfg *Config) error {
	path := GetConfigPath()
	err := os.MkdirAll(filepath.Dir(path), 0700)
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600) // WriteFile keeps the mode of an existing file (it may hold passwords)
}
