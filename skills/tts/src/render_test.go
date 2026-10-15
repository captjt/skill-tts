package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/captjt/skill-tts/internal/wav"
)

// tone returns s16le PCM: lead silence, a 440 Hz tone, trailing silence.
func tone(rate int, lead, body, tail float64) []byte {
	var pcm []byte
	n := func(sec float64) int { return int(sec * float64(rate)) }
	for range n(lead) {
		pcm = append(pcm, 0, 0)
	}
	for i := range n(body) {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
	}
	for range n(tail) {
		pcm = append(pcm, 0, 0)
	}
	return pcm
}

// fakeKokoro serves 0.2s silence + 1s tone + 0.3s silence per request.
func fakeKokoro(t *testing.T) *int32 {
	t.Helper()
	var calls int32
	pcm := tone(24000, 0.2, 1.0, 0.3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "audio/pcm")
		w.Write(pcm)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KOKORO_BASE_URL", srv.URL+"/v1")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TTS_PROVIDER", "")
	return &calls
}

func runRender(t *testing.T, args ...string) manifest {
	t.Helper()
	stdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := cmdRender(args)
	w.Close()
	os.Stdout = stdout
	out, _ := io.ReadAll(r)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("stdout is not a manifest: %v\n%s", err, out)
	}
	return m
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "script.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRenderTimingsTrimCacheAndCaptions(t *testing.T) {
	calls := fakeKokoro(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	script := writeScript(t, dir, `{
		"provider": "kokoro", "pause": 0.5,
		"segments": [
			{"id": "intro", "text": "Meet the **new** dashboard. It is fast."},
			{"id": "outro", "text": "Available today.", "pause": 2}
		]}`)

	m := runRender(t, script, "-o", out)
	if *calls != 2 || len(m.Segments) != 2 {
		t.Fatalf("calls=%d segments=%d", *calls, len(m.Segments))
	}
	a, b := m.Segments[0], m.Segments[1]
	// 1.0s of tone plus 80ms of padding on each side after trimming.
	if math.Abs(a.Duration-1.16) > 0.01 {
		t.Errorf("trimmed duration = %.3f, want ~1.16", a.Duration)
	}
	if a.Start != 0 || math.Abs(b.Start-(a.End+0.5)) > 0.002 || math.Abs(m.Duration-b.End) > 0.002 {
		t.Errorf("timings: a=%+v b=%+v total=%.3f", a, b, m.Duration)
	}
	if a.Text != "Meet the new dashboard. It is fast." {
		t.Errorf("markdown not cleaned: %q", a.Text)
	}
	if m.Combined == nil || math.Abs(m.Combined.Duration-m.Duration) > 0.002 {
		t.Errorf("combined = %+v, total %.3f", m.Combined, m.Duration)
	}
	for _, f := range []string{"intro.wav", "outro.wav", "voiceover.wav", "voiceover.srt", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	fh, _ := os.Open(a.File)
	rate, _, data, err := wav.Data(fh)
	pcm, _ := io.ReadAll(data)
	fh.Close()
	if err != nil || rate != 24000 || math.Abs(float64(len(pcm))/48000-a.Duration) > 0.001 {
		t.Errorf("segment file: rate=%d len=%d err=%v", rate, len(pcm), err)
	}
	srt, _ := os.ReadFile(m.Captions)
	if !strings.Contains(string(srt), "1\n00:00:00,000 --> ") || !strings.Contains(string(srt), "It is fast.") || strings.Count(string(srt), " --> ") != 3 {
		t.Errorf("captions:\n%s", srt)
	}

	// Re-render: nothing changed, so no requests.
	m2 := runRender(t, script, "-o", out)
	if *calls != 2 || !m2.Segments[0].Cached || !m2.Segments[1].Cached {
		t.Fatalf("cache miss: calls=%d", *calls)
	}
	// Change one segment's voice: only it re-renders.
	writeScript(t, dir, `{"provider": "kokoro", "pause": 0.5, "segments": [
		{"id": "intro", "text": "Meet the **new** dashboard. It is fast."},
		{"id": "outro", "text": "Available today.", "voice": "am_michael", "pause": 2}]}`)
	m3 := runRender(t, script, "-o", out)
	if *calls != 3 || !m3.Segments[0].Cached || m3.Segments[1].Cached {
		t.Fatalf("partial re-render: calls=%d cached=%v,%v", *calls, m3.Segments[0].Cached, m3.Segments[1].Cached)
	}
	runRender(t, script, "-o", out, "--force")
	if *calls != 5 {
		t.Fatalf("--force: calls=%d", *calls)
	}
}

func TestRenderScriptValidation(t *testing.T) {
	fakeKokoro(t)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no segments":  `{"segments": []}`,
		"unknown key":  `{"segments": [{"id": "a", "text": "x", "voic": "y"}]}`,
		"bad id":       `{"segments": [{"id": "../etc", "text": "x"}]}`,
		"reserved id":  `{"segments": [{"id": "voiceover", "text": "x"}]}`,
		"duplicate id": `{"segments": [{"id": "a", "text": "x"}, {"id": "a", "text": "y"}]}`,
		"empty text":   `{"segments": [{"id": "a", "text": "  "}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := cmdRender([]string{writeScript(t, dir, body), "-o", filepath.Join(dir, "o")}); err == nil {
				t.Fatal("accepted invalid script")
			}
		})
	}
}

func TestTrimSilenceKeepsPadding(t *testing.T) {
	pcm := tone(24000, 0.5, 0.25, 0.5)
	got := wav.TrimSilence(pcm, 24000, 1, 0)
	if d := float64(len(got)) / 48000; math.Abs(d-0.25) > 0.002 {
		t.Errorf("trimmed to %.3fs, want 0.25s", d)
	}
	if got := wav.TrimSilence(make([]byte, 4800), 24000, 1, 0); len(got) != 0 {
		t.Errorf("all-silence input kept %d bytes", len(got))
	}
}

func TestSRTTime(t *testing.T) {
	if got := srtTime(3723.0456); got != "01:02:03,046" {
		t.Errorf("srtTime = %s", got)
	}
}
