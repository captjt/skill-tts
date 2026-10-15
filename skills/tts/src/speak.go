package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/captjt/skill-tts/internal/audio"
	"github.com/captjt/skill-tts/internal/config"
	"github.com/captjt/skill-tts/internal/pipeline"
	"github.com/captjt/skill-tts/internal/provider"
	"github.com/captjt/skill-tts/internal/secrets"
	"github.com/captjt/skill-tts/internal/session"
	"github.com/captjt/skill-tts/internal/textprep"
	"github.com/captjt/skill-tts/internal/wav"
)

// job is everything a (possibly detached) worker needs to speak. It travels
// to the worker over a private stdin pipe, never via argv or disk.
type job struct {
	Provider string           `json:"provider"`
	APIKey   string           `json:"api_key,omitempty"`
	BaseURL  string           `json:"base_url,omitempty"`
	Request  provider.Request `json:"request"`
	Queue    bool             `json:"queue,omitempty"`
}

type speakOpts struct {
	provider, voice, model, speed, instructions, out, maxChars string
	wait, queue, raw, json                                     bool
}

const maxInput = 4 << 20

func cmdSpeak(args []string) error {
	var o speakOpts
	fs := newFlags("speak")
	stringFlag(fs, &o.provider, "p", "provider")
	stringFlag(fs, &o.voice, "v", "voice")
	stringFlag(fs, &o.model, "m", "model")
	stringFlag(fs, &o.speed, "s", "speed")
	stringFlag(fs, &o.instructions, "i", "instructions")
	stringFlag(fs, &o.out, "o", "out")
	fs.StringVar(&o.maxChars, "max-chars", "", "")
	boolFlag(fs, &o.wait, "w", "wait")
	boolFlag(fs, &o.queue, "q", "queue")
	boolFlag(fs, &o.raw, "", "raw")
	boolFlag(fs, &o.json, "", "json")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	text := strings.Join(pos, " ")
	if text == "" && !term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, maxInput))
		if err != nil {
			return err
		}
		text = string(b)
	}
	if !utf8.ValidString(text) {
		return errors.New("text is not valid UTF-8")
	}
	if !o.raw {
		text = textprep.Clean(text)
	}
	if strings.TrimSpace(text) == "" {
		return usagef("nothing to say (pass text as arguments or on stdin)")
	}

	j, info, err := resolveJob(o, text)
	if err != nil {
		return err
	}
	if o.out != "" {
		ctx, stop := signalContext()
		defer stop()
		return writeFile(ctx, j, info, o.out, o.json)
	}
	if o.wait {
		ctx, stop := signalContext()
		defer stop()
		start := time.Now()
		var ttfa time.Duration
		if err := runJob(ctx, j, info, func(d time.Duration) { ttfa = d }); err != nil {
			return err
		}
		fmt.Printf("Spoke %d chars via %s in %.1fs (first audio after %dms).\n",
			utf8.RuneCountInString(text), describe(j), time.Since(start).Seconds(), ttfa.Milliseconds())
		return nil
	}
	return detach(j)
}

func resolveJob(o speakOpts, text string) (job, provider.Info, error) {
	cfg, err := config.Load()
	if err != nil {
		return job{}, provider.Info{}, err
	}
	name := first(o.provider, os.Getenv("TTS_PROVIDER"), cfg.Provider, "system")
	j, info, err := buildJob(cfg, name, o)
	if err != nil {
		return j, info, err
	}
	j.Request.Text = text
	if err := checkLimit(cfg, o, info, utf8.RuneCountInString(text)); err != nil {
		return j, info, err
	}
	return j, info, nil
}

// buildJob resolves provider settings: flags, then config, then provider
// defaults. The API key is looked up here so a missing key fails fast.
func buildJob(cfg config.Config, name string, o speakOpts) (job, provider.Info, error) {
	info, err := lookupProvider(name)
	if err != nil {
		return job{}, info, err
	}
	pc := cfg.For(name)
	speed := pc.Speed
	if o.speed != "" {
		if speed, err = config.ParseSpeed(o.speed); err != nil {
			return job{}, info, usageError{err}
		}
	}
	j := job{
		Provider: name,
		BaseURL:  first(envOrEmpty(info.BaseURLEnv), pc.BaseURL),
		Queue:    o.queue,
		Request: provider.Request{
			Voice:        first(o.voice, pc.Voice, info.DefaultVoice),
			Model:        first(o.model, pc.Model, info.DefaultModel),
			Speed:        speed,
			Instructions: first(o.instructions, pc.Instructions),
		},
	}
	if info.NeedsKey() {
		key, _ := secrets.Lookup(name, info.KeyEnv)
		if key == "" {
			return j, info, fmt.Errorf("no API key for %s: set %s, or run `tts auth set %s` in a terminal", name, info.KeyEnv[0], name)
		}
		j.APIKey = key
	}
	return j, info, nil
}

// checkLimit guards against accidentally sending huge text to a paid provider.
func checkLimit(cfg config.Config, o speakOpts, info provider.Info, chars int) error {
	if info.Local {
		return nil
	}
	limit := cfg.MaxChars
	if o.maxChars != "" {
		var err error
		if limit, err = strconv.Atoi(o.maxChars); err != nil || limit <= 0 {
			return usagef("--max-chars must be a positive integer")
		}
	}
	if limit == 0 {
		limit = config.DefaultMaxChars
	}
	if chars > limit {
		return fmt.Errorf("text is %d characters, over the %d-character limit for paid providers; shorten it, or pass --max-chars %d", chars, limit, chars)
	}
	return nil
}

func describe(j job) string {
	s := j.Provider
	if j.Request.Voice != "" {
		s += " (voice " + j.Request.Voice + ")"
	}
	return s
}

// runJob speaks j in this process. ready is called once audio is flowing.
func runJob(ctx context.Context, j job, info provider.Info, ready func(time.Duration)) error {
	sess, err := session.Register()
	if err != nil {
		return err
	}
	defer sess.Close()
	prov := info.New(provider.Settings{APIKey: j.APIKey, BaseURL: j.BaseURL})
	start := time.Now()

	if ds, ok := prov.(provider.DirectSpeaker); ok {
		if j.Queue {
			ready(0)
		} else if _, err := session.StopOthers(); err != nil {
			return err
		}
		if err := sess.AcquirePlayback(ctx); err != nil {
			return err
		}
		return ds.Speak(ctx, j.Request, func() {
			if !j.Queue {
				ready(time.Since(start))
			}
		})
	}

	out := audio.Open(prov.Format()) // device init overlaps the request
	chunks := textprep.ChunkFastStart(j.Request.Text, info.FastStart, prov.MaxChars())
	st := pipeline.New(ctx, prov, j.Request, chunks)
	defer st.Close()
	ttfa, err := st.WaitFirst()
	if err != nil {
		return err
	}
	if err := out.Check(); err != nil {
		return err
	}
	ready(ttfa)
	if !j.Queue {
		if _, err := session.StopOthers(); err != nil {
			return err
		}
	}
	if err := sess.AcquirePlayback(ctx); err != nil {
		return err
	}
	return out.Play(ctx, st)
}

// detach hands the job to a background worker and returns once its audio has
// started (or failed), so the calling agent isn't blocked during playback.
func detach(j job) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(session.Dir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(session.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()

	cmd := exec.Command(self, "__worker")
	cmd.Stderr = logf // stdout stays /dev/null: the caller's pipes must not be held open
	cmd.ExtraFiles = []*os.File{readyW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the caller; own process group for `tts stop`
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readyW.Close()
	err = json.NewEncoder(stdin).Encode(j)
	stdin.Close()
	if err != nil {
		return err
	}

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(readyR).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case s := <-line:
		_ = cmd.Process.Release()
		switch {
		case strings.HasPrefix(s, "ok "):
			ms, _ := strconv.Atoi(strings.TrimPrefix(s, "ok "))
			if j.Queue {
				fmt.Printf("Queued via %s; it plays after the current speech. Stop with: tts stop\n", describe(j))
			} else {
				fmt.Printf("Speaking via %s; first audio after %dms. Playback continues in the background; stop with: tts stop\n", describe(j), ms)
			}
			return nil
		case strings.HasPrefix(s, "err "):
			return errors.New(strings.TrimPrefix(s, "err "))
		default:
			b, _ := os.ReadFile(session.LogPath())
			return fmt.Errorf("speech worker exited unexpectedly: %s", strings.TrimSpace(string(b)))
		}
	case <-time.After(120 * time.Second):
		return errors.New("timed out waiting for audio; the request is still running (stop it with: tts stop)")
	}
}

func cmdWorker([]string) error {
	ready := os.NewFile(3, "ready")
	var j job
	if err := json.NewDecoder(os.Stdin).Decode(&j); err != nil {
		return err
	}
	signaled := false
	notify := func(msg string) {
		if !signaled {
			signaled = true
			fmt.Fprintln(ready, msg)
			ready.Close()
		}
	}
	ctx, stop := signalContext()
	defer stop()
	info, err := lookupProvider(j.Provider)
	if err == nil {
		err = runJob(ctx, j, info, func(d time.Duration) { notify(fmt.Sprintf("ok %d", d.Milliseconds())) })
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		notify("err " + strings.ReplaceAll(err.Error(), "\n", " "))
		fmt.Fprintf(os.Stderr, "%s %s\n", time.Now().Format(time.RFC3339), err)
		return err
	}
	notify("ok 0")
	return nil
}

func writeFile(ctx context.Context, j job, info provider.Info, path string, asJSON bool) error {
	prov := info.New(provider.Settings{APIKey: j.APIKey, BaseURL: j.BaseURL})
	f := prov.Format()
	ext := strings.ToLower(filepath.Ext(path))
	wavPath := path
	var conv func(in, out string) *exec.Cmd
	if ext != ".wav" {
		if conv = converter(ext); conv == nil {
			return fmt.Errorf("can't write %q files: install ffmpeg, or use a .wav path", ext)
		}
		dir, err := os.MkdirTemp("", "tts-out-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		wavPath = filepath.Join(dir, "speech.wav")
	}
	n, err := synthWAV(ctx, prov, j, wavPath)
	if err != nil {
		return err
	}
	if conv != nil {
		if out, err := conv(wavPath, path).CombinedOutput(); err != nil {
			return fmt.Errorf("converting to %s: %v: %s", ext, err, strings.TrimSpace(string(out)))
		}
	}
	secs := float64(n) / float64(f.SampleRate*f.Channels*2)
	if asJSON {
		abs, _ := filepath.Abs(path)
		return printJSON(map[string]any{
			"file": abs, "duration": round3(secs), "sample_rate": f.SampleRate, "channels": f.Channels,
			"provider": j.Provider, "voice": j.Request.Voice, "model": j.Request.Model,
		})
	}
	fmt.Printf("Wrote %s (%.1fs of audio via %s).\n", path, secs, describe(j))
	return nil
}

func synthWAV(ctx context.Context, prov provider.Provider, j job, path string) (int64, error) {
	f := prov.Format()
	st := pipeline.New(ctx, prov, j.Request, textprep.Chunk(j.Request.Text, prov.MaxChars()))
	defer st.Close()
	if _, err := st.WaitFirst(); err != nil {
		return 0, err
	}
	file, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	n, err := wav.Write(file, st, f.SampleRate, f.Channels)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return n, err
}

// converter returns a command that converts a WAV file to the format implied
// by ext, preferring ffmpeg and falling back to macOS afconvert.
func converter(ext string) func(in, out string) *exec.Cmd {
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		return func(in, out string) *exec.Cmd {
			return exec.Command("ffmpeg", "-y", "-loglevel", "error", "-i", in, out)
		}
	}
	if _, err := exec.LookPath("afconvert"); err != nil {
		return nil
	}
	args := map[string][]string{
		".m4a":  {"-f", "m4af", "-d", "aac"},
		".aac":  {"-f", "adts", "-d", "aac"},
		".caf":  {"-f", "caff", "-d", "LEI16"},
		".aif":  {"-f", "AIFF", "-d", "BEI16"},
		".aiff": {"-f", "AIFF", "-d", "BEI16"},
	}[ext]
	if args == nil {
		return nil
	}
	return func(in, out string) *exec.Cmd {
		return exec.Command("afconvert", append(args, in, out)...)
	}
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}
