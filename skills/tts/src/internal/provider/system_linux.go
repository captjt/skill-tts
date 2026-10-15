package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

var systemInfo = Info{
	Name:        "system",
	Description: "espeak-ng / espeak (offline, free)",
	Local:       true,
	New:         func(Settings) Provider { return espeakProvider{} },
}

type espeakProvider struct{}

// espeak-ng renders 22.05 kHz mono.
func (espeakProvider) Format() Format { return Format{SampleRate: 22050, Channels: 1} }

func (espeakProvider) MaxChars() int { return 1 << 30 }

func findEspeak() (string, error) {
	for _, name := range []string{"espeak-ng", "espeak"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("system: espeak-ng is not installed (e.g. sudo apt install espeak-ng)")
}

func (e espeakProvider) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	bin, err := findEspeak()
	if err != nil {
		return err
	}
	args := []string{"--stdout", "--stdin"}
	if req.Voice != "" {
		args = append(args, "-v", req.Voice)
	}
	if req.Speed > 0 && req.Speed != 1 {
		args = append(args, "-s", strconv.Itoa(int(math.Round(175*req.Speed))))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(req.Text) // text via stdin, never argv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("system: %w", err)
	}
	copyErr := copyWAVData(stdout, w, e.Format())
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("system: espeak: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return copyErr
}

func (espeakProvider) Voices(ctx context.Context) ([]Voice, error) {
	bin, err := findEspeak()
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, bin, "--voices").Output()
	if err != nil {
		return nil, fmt.Errorf("system: listing voices: %w", err)
	}
	// Columns: Pty Language Age/Gender VoiceName File [Other Languages]
	var voices []Voice
	for i, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 5 {
			continue
		}
		voices = append(voices, Voice{ID: f[1], Name: f[3], Note: f[2]})
	}
	return voices, nil
}

// SystemAvailable reports whether the system speech engine is installed.
func SystemAvailable() (string, bool) {
	p, err := findEspeak()
	return p, err == nil
}
