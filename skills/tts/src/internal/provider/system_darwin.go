package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var systemInfo = Info{
	Name:        "system",
	Description: "macOS built-in voices via `say` (offline, free)",
	Local:       true,
	New:         func(Settings) Provider { return sayProvider{} },
}

const sayPath = "/usr/bin/say"

type sayProvider struct{}

func (sayProvider) Format() Format { return PCM24k }

func (sayProvider) MaxChars() int { return 1 << 30 }

func (s sayProvider) args(ctx context.Context, req Request) ([]string, error) {
	voice := req.Voice
	if voice == "" {
		// The system default is often a Siri voice, which `say` renders as
		// silence; pick the best installed voice that works instead.
		voice = s.defaultVoice(ctx)
	} else if err := s.checkVoice(ctx, voice); err != nil {
		// say silently falls back to the default voice on unknown names; fail instead.
		return nil, err
	}
	args := []string{"-v", voice}
	if req.Speed > 0 && req.Speed != 1 {
		args = append(args, "-r", strconv.Itoa(int(math.Round(185*req.Speed))))
	}
	return append(args, "-f", "-"), nil // text via stdin, never argv
}

func (s sayProvider) Speak(ctx context.Context, req Request, started func()) error {
	args, err := s.args(ctx, req)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, sayPath, args...)
	cmd.Stdin = strings.NewReader(req.Text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	started()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("system: say: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s sayProvider) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	args, err := s.args(ctx, req)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "tts-say-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "out.wav")
	cmd := exec.CommandContext(ctx, sayPath, append([]string{"-o", out, "--file-format=WAVE", "--data-format=LEI16@24000"}, args...)...)
	cmd.Stdin = strings.NewReader(req.Text)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("system: say: %v: %s", err, strings.TrimSpace(string(b)))
	}
	f, err := os.Open(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return copyWAVData(f, w, PCM24k)
}

// sayVoiceLine matches `say -v ?` output: "Name (Variant)   en_US    # Sample".
var sayVoiceLine = regexp.MustCompile(`^(.+?)\s+([a-z]{2,3}_[A-Za-z0-9]+)\s+#\s?(.*)$`)

// Voices lists installed voices (always fresh; also refreshes the cache).
func (s sayProvider) Voices(ctx context.Context) ([]Voice, error) {
	return s.voices(ctx, 0)
}

// voices returns installed voices, from a cache younger than maxAge if
// possible: `say -v ?` takes ~0.5s, too slow for every utterance.
func (sayProvider) voices(ctx context.Context, maxAge time.Duration) ([]Voice, error) {
	cache := filepath.Join(cacheDir(), "say-voices.txt")
	var out []byte
	if st, err := os.Stat(cache); err == nil && maxAge > 0 && time.Since(st.ModTime()) < maxAge {
		out, _ = os.ReadFile(cache)
	}
	if len(out) == 0 {
		var err error
		if out, err = exec.CommandContext(ctx, sayPath, "-v", "?").Output(); err != nil {
			return nil, fmt.Errorf("system: listing voices: %w", err)
		}
		if os.MkdirAll(filepath.Dir(cache), 0o700) == nil {
			_ = os.WriteFile(cache, out, 0o600)
		}
	}
	var voices []Voice
	for _, line := range strings.Split(string(out), "\n") {
		if m := sayVoiceLine.FindStringSubmatch(line); m != nil {
			voices = append(voices, Voice{ID: m[1], Name: m[1], Note: m[2]})
		}
	}
	return voices, nil
}

func (s sayProvider) checkVoice(ctx context.Context, name string) error {
	for _, maxAge := range []time.Duration{24 * time.Hour, 0} { // cached, then live
		voices, err := s.voices(ctx, maxAge)
		if err != nil {
			return err
		}
		for _, v := range voices {
			if strings.EqualFold(v.ID, name) {
				return nil
			}
		}
	}
	return fmt.Errorf("system: no voice named %q (list them with: tts voices -p system)", name)
}

// defaultVoice prefers downloaded Premium/Enhanced English voices, which
// sound far better, and falls back to Samantha, which ships with macOS.
func (s sayProvider) defaultVoice(ctx context.Context) string {
	voices, _ := s.voices(ctx, 24*time.Hour)
	for _, tier := range []string{"(Premium)", "(Enhanced)"} {
		for _, locale := range []string{"en_US", "en_GB", "en_AU", "en_IE", "en_CA"} {
			for _, v := range voices {
				if v.Note == locale && strings.Contains(v.ID, tier) {
					return v.ID
				}
			}
		}
	}
	return "Samantha"
}

func cacheDir() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "skill-tts")
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "skill-tts")
	}
	return filepath.Join(d, "skill-tts")
}

// SystemAvailable reports whether the system speech engine is installed.
func SystemAvailable() (string, bool) {
	_, err := os.Stat(sayPath)
	return "say", err == nil
}
