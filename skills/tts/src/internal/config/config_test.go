package config

import (
	"os"
	"strings"
	"testing"
)

func TestSetGetSaveLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var c Config
	for k, v := range map[string]string{
		"provider":            "openai",
		"openai.voice":        "cedar",
		"openai.speed":        "1.2",
		"openai.instructions": "warm and calm",
		"kokoro.base_url":     "http://127.0.0.1:8880/v1/",
		"max_chars":           "5000",
	} {
		if err := c.Set(k, v); err != nil {
			t.Fatalf("Set(%s): %v", k, err)
		}
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", st.Mode(), err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != "openai" || got.For("openai").Voice != "cedar" || got.For("openai").Speed != 1.2 ||
		got.For("kokoro").BaseURL != "http://127.0.0.1:8880/v1" || got.MaxChars != 5000 {
		t.Fatalf("loaded %+v", got)
	}
	if err := got.Set("openai.voice", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("openai.voice"); v != "" {
		t.Fatalf("unset left %q", v)
	}
}

func TestSetRejects(t *testing.T) {
	var c Config
	for key, val := range map[string]string{
		"provider":        "nope",
		"openai.api_key":  "sk-123",
		"openai.speed":    "fast",
		"kokoro.base_url": "file:///etc/passwd",
		"bogus":           "x",
		"nope.voice":      "x",
	} {
		if err := c.Set(key, val); err == nil {
			t.Errorf("Set(%s, %s) accepted", key, val)
		}
	}
	if err := c.Set("openai.api_key", "x"); !strings.Contains(err.Error(), "auth set") {
		t.Errorf("api_key error should point to tts auth: %v", err)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load()
	if err != nil || c.Provider != "" {
		t.Fatalf("%+v %v", c, err)
	}
}
