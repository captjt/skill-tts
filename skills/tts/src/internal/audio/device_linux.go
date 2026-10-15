package audio

import (
	"errors"
	"os"
	"path/filepath"
)

// deviceCheck avoids initializing ALSA (which prints a page of errors) on
// machines with no sound server or sound card, such as servers and containers.
func deviceCheck() error {
	if os.Getenv("PULSE_SERVER") != "" {
		return nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "pulse", "native")); err == nil {
			return nil
		}
	}
	if entries, err := os.ReadDir("/dev/snd"); err == nil && len(entries) > 0 {
		return nil
	}
	return errors.New("no PulseAudio/PipeWire server or ALSA device found")
}
