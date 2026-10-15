package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/captjt/skill-tts/internal/config"
	"github.com/captjt/skill-tts/internal/pipeline"
	"github.com/captjt/skill-tts/internal/provider"
	"github.com/captjt/skill-tts/internal/textprep"
	"github.com/captjt/skill-tts/internal/wav"
)

// script is the input to `tts render`: narration split into segments (one
// per scene or beat), with defaults that individual segments may override.
type script struct {
	Provider     string    `json:"provider"`
	Voice        string    `json:"voice"`
	Model        string    `json:"model"`
	Instructions string    `json:"instructions"`
	Speed        float64   `json:"speed"`
	Pause        *float64  `json:"pause"` // seconds between segments in the combined track
	Raw          bool      `json:"raw"`
	Segments     []segment `json:"segments"`
}

type segment struct {
	ID           string   `json:"id"`
	Text         string   `json:"text"`
	Voice        string   `json:"voice"`
	Model        string   `json:"model"`
	Instructions string   `json:"instructions"`
	Speed        float64  `json:"speed"`
	Pause        *float64 `json:"pause"` // overrides the pause after this segment
}

// manifest is written to <out>/manifest.json and printed to stdout.
type manifest struct {
	Provider   string          `json:"provider"`
	SampleRate int             `json:"sample_rate"`
	Channels   int             `json:"channels"`
	Combined   *combinedTrack  `json:"combined,omitempty"`
	Captions   string          `json:"captions,omitempty"`
	Duration   float64         `json:"duration"`
	Segments   []segmentResult `json:"segments"`
}

type combinedTrack struct {
	File     string  `json:"file"`
	Duration float64 `json:"duration"`
}

type segmentResult struct {
	ID           string  `json:"id"`
	File         string  `json:"file,omitempty"`
	Duration     float64 `json:"duration"`
	Start        float64 `json:"start"` // offset in the combined track
	End          float64 `json:"end"`
	Text         string  `json:"text"`
	Voice        string  `json:"voice,omitempty"`
	Model        string  `json:"model,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	Speed        float64 `json:"speed,omitempty"`
	Hash         string  `json:"hash"`
	Cached       bool    `json:"cached"`
	Error        string  `json:"error,omitempty"`

	pcm   []byte
	pause float64
}

var segmentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const renderUsage = `Usage: tts render SCRIPT.json [-o DIR] [flags]

Renders narration for a video. SCRIPT.json (or - for stdin):

  {
    "provider": "elevenlabs", "voice": "...", "instructions": "warm, confident",
    "pause": 0.5,
    "segments": [
      {"id": "intro",   "text": "Meet the new dashboard."},
      {"id": "filters", "text": "Filter any view in one click.", "pause": 1.0}
    ]
  }

Writes DIR/<id>.wav per segment (silence-trimmed), DIR/voiceover.wav (all
segments joined with pauses), DIR/voiceover.srt captions, and DIR/manifest.json
with each segment's duration and start/end offsets. The manifest is also
printed to stdout. Unchanged segments are reused from the previous render.

Flags:
  -o, --out DIR        output directory (default ./voiceover)
  -p, -v, -m, -i, -s   provider, voice, model, instructions, speed defaults
  --format EXT         combined track format: wav (default), mp3, m4a, ...
  --pause SECONDS      default pause between segments (default 0.5)
  --no-combine         only write per-segment files
  --no-trim            keep the provider's leading/trailing silence
  --force              re-render every segment
  --jobs N             parallel requests (default 3 cloud, 1 local)
  --max-chars N        limit for the total text sent to a paid provider
`

func cmdRender(args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "-help" {
			fmt.Print(renderUsage)
			return nil
		}
	}
	var o speakOpts
	var outDir, format, pause, jobs string
	var noCombine, noTrim, force bool
	fs := newFlags("render")
	stringFlag(fs, &o.provider, "p", "provider")
	stringFlag(fs, &o.voice, "v", "voice")
	stringFlag(fs, &o.model, "m", "model")
	stringFlag(fs, &o.speed, "s", "speed")
	stringFlag(fs, &o.instructions, "i", "instructions")
	stringFlag(fs, &outDir, "o", "out")
	fs.StringVar(&o.maxChars, "max-chars", "", "")
	fs.StringVar(&format, "format", "wav", "")
	fs.StringVar(&pause, "pause", "", "")
	fs.StringVar(&jobs, "jobs", "", "")
	boolFlag(fs, &noCombine, "", "no-combine")
	boolFlag(fs, &noTrim, "", "no-trim")
	boolFlag(fs, &force, "", "force")
	boolFlag(fs, &o.raw, "", "raw")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fmt.Fprint(os.Stderr, renderUsage)
		return usagef("render takes one script file (or - for stdin)")
	}

	sc, err := loadScript(pos[0])
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name := first(o.provider, sc.Provider, os.Getenv("TTS_PROVIDER"), cfg.VoiceoverProvider, cfg.Provider, "system")
	if info, ok := provider.Lookup(name); ok {
		// Voiceovers favor fidelity over latency unless a model was chosen.
		o.model = first(o.model, sc.Model, cfg.For(name).Model, info.RenderModel)
	}
	o.voice, o.model, o.instructions = first(o.voice, sc.Voice), first(o.model, sc.Model), first(o.instructions, sc.Instructions)
	if o.speed == "" && sc.Speed != 0 {
		o.speed = strconv.FormatFloat(sc.Speed, 'f', -1, 64)
	}
	base, info, err := buildJob(cfg, name, o)
	if err != nil {
		return err
	}
	defPause := 0.5
	if sc.Pause != nil {
		defPause = *sc.Pause
	}
	if pause != "" {
		if defPause, err = strconv.ParseFloat(pause, 64); err != nil || defPause < 0 {
			return usagef("--pause must be a non-negative number of seconds")
		}
	}
	workers := 3
	if info.Local {
		workers = 1
	}
	if jobs != "" {
		if workers, err = strconv.Atoi(jobs); err != nil || workers < 1 {
			return usagef("--jobs must be a positive integer")
		}
	}
	if outDir == "" {
		outDir = "voiceover"
	}
	if outDir, err = filepath.Abs(outDir); err != nil {
		return err
	}
	format = strings.TrimPrefix(strings.ToLower(format), ".")
	if format != "wav" && converter("."+format) == nil {
		return fmt.Errorf("can't write %s files: install ffmpeg, or use --format wav", format)
	}

	// Resolve every segment's request and cache key.
	prov := info.New(provider.Settings{APIKey: base.APIKey, BaseURL: base.BaseURL})
	f := prov.Format()
	prev := previousManifest(outDir)
	results := make([]*segmentResult, len(sc.Segments))
	newChars := 0
	for i, s := range sc.Segments {
		text := s.Text
		if !sc.Raw && !o.raw {
			text = textprep.Clean(text)
		}
		req := base.Request
		req.Text = text
		req.Voice = first(s.Voice, req.Voice)
		req.Model = first(s.Model, req.Model)
		req.Instructions = first(s.Instructions, req.Instructions)
		if s.Speed != 0 {
			if _, err := config.ParseSpeed(strconv.FormatFloat(s.Speed, 'f', -1, 64)); err != nil {
				return fmt.Errorf("segment %q: %w", s.ID, err)
			}
			req.Speed = s.Speed
		}
		r := &segmentResult{
			ID: s.ID, Text: text, Voice: req.Voice, Model: req.Model, Instructions: req.Instructions, Speed: req.Speed,
			File:  filepath.Join(outDir, s.ID+".wav"),
			Hash:  segmentHash(name, base.BaseURL, req, !noTrim),
			pause: defPause,
		}
		if s.Pause != nil {
			r.pause = *s.Pause
		}
		if p, ok := prev[s.ID]; ok && !force && p.Hash == r.Hash {
			if pcm, err := readPCM(r.File, f); err == nil {
				r.pcm, r.Cached = pcm, true
			}
		}
		if !r.Cached {
			newChars += utf8.RuneCountInString(text)
		}
		results[i] = r
	}
	if err := checkLimit(cfg, o, info, newChars); err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()
	failed := renderSegments(ctx, prov, base, results, workers, !noTrim)

	m := manifest{Provider: name, SampleRate: f.SampleRate, Channels: f.Channels}
	t := 0.0 // exact offsets from sample counts; rounded only for output
	for i, r := range results {
		if r.Error == "" {
			d := pcmSeconds(r.pcm, f)
			r.Duration, r.Start, r.End = round3(d), round3(t), round3(t+d)
			t += d
			if i < len(results)-1 {
				t += float64(pauseSamples(r.pause, f)) / float64(f.SampleRate)
			}
		}
		m.Segments = append(m.Segments, *r)
	}
	m.Duration = round3(t)
	if failed == 0 && !noCombine {
		if m.Combined, err = writeCombined(outDir, format, results, f); err != nil {
			return err
		}
		m.Captions = filepath.Join(outDir, "voiceover.srt")
		if err := os.WriteFile(m.Captions, []byte(captions(m.Segments)), 0o644); err != nil {
			return err
		}
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d segments failed (details in %s); re-run to retry only those", failed, len(results), filepath.Join(outDir, "manifest.json"))
	}
	fmt.Println(string(b))
	return nil
}

func loadScript(path string) (script, error) {
	var sc script
	var rd io.Reader = os.Stdin
	if path != "-" {
		fh, err := os.Open(path)
		if err != nil {
			return sc, err
		}
		defer fh.Close()
		rd = fh
	}
	dec := json.NewDecoder(io.LimitReader(rd, maxInput))
	dec.DisallowUnknownFields() // catch typos like "voic" instead of silently ignoring them
	if err := dec.Decode(&sc); err != nil {
		return sc, fmt.Errorf("reading script: %w", err)
	}
	if len(sc.Segments) == 0 {
		return sc, errors.New("script has no segments")
	}
	seen := map[string]bool{}
	for i := range sc.Segments {
		s := &sc.Segments[i]
		if s.ID == "" {
			s.ID = fmt.Sprintf("%02d", i+1)
		}
		if !segmentID.MatchString(s.ID) || s.ID == "voiceover" || s.ID == "manifest" {
			return sc, fmt.Errorf("segment id %q: use letters, digits, '-', '_' or '.' (and not voiceover/manifest)", s.ID)
		}
		if seen[s.ID] {
			return sc, fmt.Errorf("duplicate segment id %q", s.ID)
		}
		seen[s.ID] = true
		if strings.TrimSpace(s.Text) == "" {
			return sc, fmt.Errorf("segment %q has no text", s.ID)
		}
		if s.Pause != nil && *s.Pause < 0 {
			return sc, fmt.Errorf("segment %q: pause must not be negative", s.ID)
		}
	}
	return sc, nil
}

// renderSegments synthesizes uncached segments with bounded parallelism and
// returns how many failed. Rate limits and server errors are retried.
func renderSegments(ctx context.Context, prov provider.Provider, base job, results []*segmentResult, workers int, trim bool) int {
	f := prov.Format()
	var todo []*segmentResult
	for _, r := range results {
		if !r.Cached {
			todo = append(todo, r)
		}
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, failed := len(results)-len(todo), 0
	for _, r := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			req := base.Request
			req.Text, req.Voice, req.Model, req.Instructions, req.Speed = r.Text, r.Voice, r.Model, r.Instructions, r.Speed
			pcm, err := synthesizeWithRetry(ctx, prov, req)
			if err == nil && trim {
				pcm = wav.TrimSilence(pcm, f.SampleRate, f.Channels, 80*time.Millisecond)
			}
			if err == nil && len(pcm) == 0 {
				err = errors.New("provider returned only silence")
			}
			if err == nil {
				err = writeWAVFile(r.File, pcm, f)
			}
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				failed++
				r.Error = err.Error()
				fmt.Fprintf(os.Stderr, "[%d/%d] %s: FAILED: %v\n", done, len(results), r.ID, err)
				return
			}
			r.pcm = pcm
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %.2fs\n", done, len(results), r.ID, pcmSeconds(pcm, f))
		}()
	}
	wg.Wait()
	return failed
}

func synthesizeWithRetry(ctx context.Context, prov provider.Provider, req provider.Request) ([]byte, error) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		var buf bytes.Buffer
		st := pipeline.New(ctx, prov, req, textprep.Chunk(req.Text, prov.MaxChars()))
		_, err := io.Copy(&buf, st)
		st.Close()
		if err == nil {
			return buf.Bytes(), nil
		}
		var apiErr *provider.APIError
		retryable := errors.As(err, &apiErr) && (apiErr.Status == 429 || apiErr.Status >= 500)
		if !retryable || attempt == 4 {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}
}

// segmentHash identifies the audio a segment would produce, so unchanged
// segments can be reused instead of re-rendered (and re-billed).
func segmentHash(providerName, baseURL string, req provider.Request, trim bool) string {
	b, _ := json.Marshal([]any{"v1", providerName, baseURL, req.Model, req.Voice, req.Instructions, req.Speed, trim, req.Text})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func previousManifest(dir string) map[string]segmentResult {
	out := map[string]segmentResult{}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return out
	}
	var m manifest
	if json.Unmarshal(b, &m) != nil {
		return out
	}
	for _, s := range m.Segments {
		if s.Error == "" {
			out[s.ID] = s
		}
	}
	return out
}

func readPCM(path string, want provider.Format) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	rate, ch, data, err := wav.Data(fh)
	if err != nil {
		return nil, err
	}
	if rate != want.SampleRate || ch != want.Channels {
		return nil, errors.New("format changed")
	}
	return io.ReadAll(data)
}

// writeWAVFile writes atomically so an interrupted render never leaves a
// truncated file that a later run would mistake for a cached segment.
func writeWAVFile(path string, pcm []byte, f provider.Format) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.wav")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = wav.Write(tmp, bytes.NewReader(pcm), f.SampleRate, f.Channels)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func writeCombined(dir, format string, results []*segmentResult, f provider.Format) (*combinedTrack, error) {
	var all bytes.Buffer
	for i, r := range results {
		all.Write(r.pcm)
		if i < len(results)-1 {
			all.Write(make([]byte, pauseSamples(r.pause, f)*f.Channels*2))
		}
	}
	wavPath := filepath.Join(dir, "voiceover.wav")
	if err := writeWAVFile(wavPath, all.Bytes(), f); err != nil {
		return nil, err
	}
	out := &combinedTrack{File: wavPath, Duration: round3(pcmSeconds(all.Bytes(), f))}
	if format != "wav" {
		out.File = filepath.Join(dir, "voiceover."+format)
		if b, err := converter("."+format)(wavPath, out.File).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("converting to %s: %v: %s", format, err, strings.TrimSpace(string(b)))
		}
		os.Remove(wavPath)
	}
	return out, nil
}

// captions builds SRT cues, one per sentence. Sentence timing within a
// segment is estimated from character counts; segment boundaries are exact.
func captions(segs []segmentResult) string {
	var b strings.Builder
	n := 0
	for _, s := range segs {
		sentences := splitSentences(s.Text)
		total := 0
		for _, sent := range sentences {
			total += utf8.RuneCountInString(sent)
		}
		t := s.Start
		for _, sent := range sentences {
			d := (s.End - s.Start) * float64(utf8.RuneCountInString(sent)) / float64(max(total, 1))
			n++
			fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", n, srtTime(t), srtTime(t+d), strings.Join(strings.Fields(sent), " "))
			t += d
		}
	}
	return b.String()
}

var sentenceSplit = regexp.MustCompile(`[^.!?…]+[.!?…]+["'”’)\]]*|[^.!?…]+$`)

func splitSentences(text string) []string {
	var out []string
	for _, s := range sentenceSplit.FindAllString(text, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{strings.TrimSpace(text)}
	}
	return out
}

func srtTime(sec float64) string {
	ms := int64(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

func pauseSamples(sec float64, f provider.Format) int { return int(sec*float64(f.SampleRate) + 0.5) }

func pcmSeconds(pcm []byte, f provider.Format) float64 {
	return float64(len(pcm)) / float64(f.SampleRate*f.Channels*2)
}

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
