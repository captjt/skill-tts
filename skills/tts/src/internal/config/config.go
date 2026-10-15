// Package config loads user preferences from
// $XDG_CONFIG_HOME/skill-tts/config.json (default ~/.config/skill-tts/config.json).
// API keys are deliberately not stored here; see package secrets.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/captjt/skill-tts/internal/provider"
)

// ProviderConfig holds per-provider defaults.
type ProviderConfig struct {
	Voice        string  `json:"voice,omitempty"`
	Model        string  `json:"model,omitempty"`
	Speed        float64 `json:"speed,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	BaseURL      string  `json:"base_url,omitempty"`
}

// Config is the on-disk configuration.
type Config struct {
	Provider          string                    `json:"provider,omitempty"`
	VoiceoverProvider string                    `json:"voiceover_provider,omitempty"` // default for `tts render`
	MaxChars          int                       `json:"max_chars,omitempty"`
	Providers         map[string]ProviderConfig `json:"providers,omitempty"`
}

// DefaultMaxChars caps text sent to paid providers unless overridden.
const DefaultMaxChars = 20000

// Path returns the config file location.
func Path() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "skill-tts", "config.json")
}

// Load reads the config file; a missing file yields an empty config.
func Load() (Config, error) {
	var c Config
	b, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", Path(), err)
	}
	return c, nil
}

// Save writes the config file (0600) atomically.
func (c Config) Save() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// For returns the stored settings for a provider.
func (c Config) For(name string) ProviderConfig { return c.Providers[name] }

// Keys lists the settable keys, for help text.
func Keys() []string {
	keys := []string{"provider", "voiceover_provider", "max_chars"}
	for _, p := range provider.Names() {
		for _, f := range []string{"voice", "model", "speed", "instructions", "base_url"} {
			keys = append(keys, p+"."+f)
		}
	}
	return keys
}

// Get returns the value for key ("" if unset).
func (c Config) Get(key string) (string, error) {
	switch key {
	case "provider":
		return c.Provider, nil
	case "voiceover_provider":
		return c.VoiceoverProvider, nil
	case "max_chars":
		if c.MaxChars == 0 {
			return "", nil
		}
		return strconv.Itoa(c.MaxChars), nil
	}
	name, field, err := splitKey(key)
	if err != nil {
		return "", err
	}
	pc := c.Providers[name]
	switch field {
	case "voice":
		return pc.Voice, nil
	case "model":
		return pc.Model, nil
	case "speed":
		if pc.Speed == 0 {
			return "", nil
		}
		return strconv.FormatFloat(pc.Speed, 'f', -1, 64), nil
	case "instructions":
		return pc.Instructions, nil
	default:
		return pc.BaseURL, nil
	}
}

// Set assigns key = value. An empty value unsets the key.
func (c *Config) Set(key, value string) error {
	switch key {
	case "provider", "voiceover_provider":
		if _, ok := provider.Lookup(value); !ok && value != "" {
			return fmt.Errorf("unknown provider %q (choose from: %s)", value, strings.Join(provider.Names(), ", "))
		}
		if key == "provider" {
			c.Provider = value
		} else {
			c.VoiceoverProvider = value
		}
		return nil
	case "max_chars":
		if value == "" {
			c.MaxChars = 0
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("max_chars must be a positive integer")
		}
		c.MaxChars = n
		return nil
	}
	name, field, err := splitKey(key)
	if err != nil {
		return err
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	pc := c.Providers[name]
	switch field {
	case "voice":
		pc.Voice = value
	case "model":
		pc.Model = value
	case "speed":
		if value == "" {
			pc.Speed = 0
			break
		}
		f, err := ParseSpeed(value)
		if err != nil {
			return err
		}
		pc.Speed = f
	case "instructions":
		pc.Instructions = value
	case "base_url":
		if value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
			return fmt.Errorf("base_url must start with http:// or https://")
		}
		pc.BaseURL = strings.TrimRight(value, "/")
	}
	if pc == (ProviderConfig{}) {
		delete(c.Providers, name)
	} else {
		c.Providers[name] = pc
	}
	return nil
}

// ParseSpeed parses a speed multiplier in a sane range.
func ParseSpeed(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0.25 || f > 4 {
		return 0, fmt.Errorf("speed must be a number between 0.25 and 4 (1 = normal)")
	}
	return f, nil
}

func splitKey(key string) (name, field string, err error) {
	name, field, ok := strings.Cut(key, ".")
	if !ok {
		return "", "", unknownKey(key)
	}
	if _, known := provider.Lookup(name); !known {
		return "", "", unknownKey(key)
	}
	switch field {
	case "voice", "model", "speed", "instructions", "base_url":
		return name, field, nil
	case "api_key", "key", "apikey":
		return "", "", fmt.Errorf("API keys are not stored in the config file; use an env var or: tts auth set %s", name)
	}
	return "", "", unknownKey(key)
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q (keys: provider, voiceover_provider, max_chars, <provider>.voice|model|speed|instructions|base_url)", key)
}

// Flatten returns all set keys in sorted order, for display.
func (c Config) Flatten() [][2]string {
	var out [][2]string
	for _, k := range Keys() {
		if v, _ := c.Get(k); v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
