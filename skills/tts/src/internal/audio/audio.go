// Package audio plays raw 16-bit PCM streams.
//
// The built-in backend uses oto (CoreAudio on macOS, PulseAudio/PipeWire or
// ALSA on Linux) in-process, with no cgo. If it cannot start, an installed
// command-line player is used instead.
package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ebitengine/oto/v3"

	"github.com/captjt/skill-tts/internal/provider"
	"github.com/captjt/skill-tts/internal/wav"
)

// Output is an audio device opened for one PCM format.
type Output struct {
	format provider.Format
	ready  chan struct{}
	ctx    *oto.Context
	err    error
}

// Open starts initializing the audio device in the background, so it can
// overlap with the network request. Set TTS_PLAYER to force an external
// player (e.g. "ffplay").
func Open(f provider.Format) *Output {
	o := &Output{format: f, ready: make(chan struct{})}
	if os.Getenv("TTS_PLAYER") != "" && os.Getenv("TTS_PLAYER") != "builtin" {
		o.err = errors.New("built-in output disabled by TTS_PLAYER")
		close(o.ready)
		return o
	}
	if err := deviceCheck(); err != nil {
		o.err = err
		close(o.ready)
		return o
	}
	go func() {
		defer close(o.ready)
		ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
			SampleRate:   f.SampleRate,
			ChannelCount: f.Channels,
			Format:       oto.FormatSignedInt16LE,
		})
		if err != nil {
			o.err = err
			return
		}
		<-ready
		if err := ctx.Err(); err != nil {
			o.err = err
			return
		}
		o.ctx = ctx
	}()
	return o
}

// Check returns an error if there is no way to play audio at all.
func (o *Output) Check() error {
	<-o.ready
	if o.err != nil && findExternal() == nil {
		return fmt.Errorf("no audio output: %v; use --out to write a file instead", o.err)
	}
	return nil
}

// Backend names the output that Play will use.
func (o *Output) Backend() string {
	<-o.ready
	if o.err == nil {
		return builtinName
	}
	if p := findExternal(); p != nil {
		return p.name + " (external)"
	}
	return "none: " + o.err.Error()
}

// Play plays PCM from r until EOF and the audio has drained, or ctx is done.
func (o *Output) Play(ctx context.Context, r io.Reader) error {
	<-o.ready
	if o.err == nil {
		return o.playBuiltin(ctx, r)
	}
	p := findExternal()
	if p == nil {
		return fmt.Errorf("no audio output: %v; install ffplay, mpv, or sox, or use --out to write a file", o.err)
	}
	return p.play(ctx, r, o.format)
}

func (o *Output) playBuiltin(ctx context.Context, r io.Reader) error {
	// A little trailing silence lets the device drain the last real samples
	// before we report completion and exit.
	tail := make([]byte, o.format.SampleRate*o.format.Channels*2/4)
	p := o.ctx.NewPlayer(io.MultiReader(r, bytes.NewReader(tail)))
	p.Play()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			p.Pause()
			return ctx.Err()
		case <-tick.C:
			if !p.IsPlaying() {
				return p.Err()
			}
		}
	}
}

type external struct {
	name string
	args func(provider.Format) []string
	file bool // needs a WAV file rather than stdin
}

func itoa(n int) string { return fmt.Sprint(n) }

var externals = []external{
	{name: "ffplay", args: func(f provider.Format) []string {
		return []string{"-nodisp", "-autoexit", "-loglevel", "error", "-f", "s16le", "-ar", itoa(f.SampleRate), "-ch_layout", channelLayout(f.Channels), "-i", "-"}
	}},
	{name: "mpv", args: func(f provider.Format) []string {
		return []string{"--no-terminal", "--no-video", "--demuxer=rawaudio", "--demuxer-rawaudio-format=s16le",
			"--demuxer-rawaudio-rate=" + itoa(f.SampleRate), "--demuxer-rawaudio-channels=" + itoa(f.Channels), "-"}
	}},
	{name: "paplay", args: func(f provider.Format) []string {
		return []string{"--raw", "--format=s16le", "--rate=" + itoa(f.SampleRate), "--channels=" + itoa(f.Channels)}
	}},
	{name: "aplay", args: func(f provider.Format) []string {
		return []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", itoa(f.SampleRate), "-c", itoa(f.Channels), "-"}
	}},
	{name: "play", args: func(f provider.Format) []string { // sox
		return []string{"-q", "-t", "raw", "-r", itoa(f.SampleRate), "-e", "signed-integer", "-b", "16", "-c", itoa(f.Channels), "-"}
	}},
	{name: "afplay", file: true, args: func(provider.Format) []string { return nil }}, // macOS, buffers whole file
}

func channelLayout(n int) string {
	if n == 2 {
		return "stereo"
	}
	return "mono"
}

func findExternal() *external {
	want := os.Getenv("TTS_PLAYER")
	for i := range externals {
		e := &externals[i]
		if want != "" && want != "builtin" && want != e.name {
			continue
		}
		if _, err := exec.LookPath(e.name); err == nil {
			return e
		}
	}
	return nil
}

func (e *external) play(ctx context.Context, r io.Reader, f provider.Format) error {
	if e.file {
		dir, err := os.MkdirTemp("", "tts-play-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "speech.wav")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, err = wav.Write(file, r, f.SampleRate, f.Channels)
		if cerr := file.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		return exec.CommandContext(ctx, e.name, append(e.args(f), path)...).Run()
	}
	cmd := exec.CommandContext(ctx, e.name, e.args(f)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, copyErr := io.Copy(stdin, r)
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w", e.name, err)
	}
	return copyErr
}

var builtinName = map[string]string{
	"darwin": "CoreAudio (built-in)",
	"linux":  "PulseAudio/ALSA (built-in)",
}[runtime.GOOS]
