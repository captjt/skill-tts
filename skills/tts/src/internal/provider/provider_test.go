package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captured is one request seen by the fake server.
type captured struct {
	method, path, query string
	header              http.Header
	body                map[string]any
}

func fakeServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]captured) {
	t.Helper()
	var reqs []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := captured{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone()}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			if err := json.Unmarshal(b, &c.body); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
		}
		reqs = append(reqs, c)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func pcmHandler(pcm []byte) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Write(pcm)
	}
}

func newFor(t *testing.T, name, baseURL string) Provider {
	t.Helper()
	info, ok := Lookup(name)
	if !ok {
		t.Fatalf("no provider %q", name)
	}
	return info.New(Settings{APIKey: "test-key", BaseURL: baseURL})
}

func synth(t *testing.T, p Provider, req Request) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := p.Synthesize(context.Background(), req, &out)
	return out.Bytes(), err
}

func TestOpenAIRequest(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	srv, reqs := fakeServer(t, pcmHandler(pcm))
	got, err := synth(t, newFor(t, "openai", srv.URL+"/v1"), Request{
		Text: "hello", Voice: "marin", Model: "gpt-4o-mini-tts", Speed: 1.25, Instructions: "calm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pcm) {
		t.Errorf("audio = %v, want %v", got, pcm)
	}
	r := (*reqs)[0]
	if r.path != "/v1/audio/speech" || r.header.Get("Authorization") != "Bearer test-key" {
		t.Errorf("path %q auth %q", r.path, r.header.Get("Authorization"))
	}
	want := map[string]any{"model": "gpt-4o-mini-tts", "input": "hello", "voice": "marin", "response_format": "pcm", "speed": 1.25, "instructions": "calm"}
	assertBody(t, r.body, want)
}

func TestOpenAIOmitsInstructionsForTTS1(t *testing.T) {
	srv, reqs := fakeServer(t, pcmHandler([]byte{0, 0}))
	if _, err := synth(t, newFor(t, "openai", srv.URL), Request{Text: "x", Voice: "alloy", Model: "tts-1", Instructions: "calm"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := (*reqs)[0].body["instructions"]; ok {
		t.Error("instructions sent to tts-1")
	}
}

func TestKokoroNeedsNoKeyAndParsesVoiceObjects(t *testing.T) {
	srv, reqs := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/audio/voices" {
			fmt.Fprint(w, `{"voices":[{"id":"af_heart","overall_grade":"A"},"bm_george"]}`)
			return
		}
		pcmHandler([]byte{9, 9})(w, r)
	})
	info, _ := Lookup("kokoro")
	p := info.New(Settings{BaseURL: srv.URL + "/v1"})
	if _, err := synth(t, p, Request{Text: "hi", Voice: "af_heart", Model: "kokoro", Instructions: "ignored"}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.header.Get("Authorization") != "" {
		t.Error("kokoro sent an Authorization header")
	}
	if _, ok := r.body["instructions"]; ok {
		t.Error("kokoro sent instructions")
	}
	voices, err := p.(VoiceLister).Voices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(voices) != 2 || voices[0].ID != "af_heart" || !strings.Contains(voices[0].Note, "grade A") || voices[1].ID != "bm_george" {
		t.Errorf("voices = %+v", voices)
	}
}

func TestElevenLabsRequestAndNameResolution(t *testing.T) {
	srv, reqs := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/voices" {
			fmt.Fprint(w, `{"voices":[{"voice_id":"AAAAAAAAAAAAAAAAAAAA","name":"Rachel"},{"voice_id":"BBBBBBBBBBBBBBBBBBBB","name":"George - warm narrator"}]}`)
			return
		}
		pcmHandler([]byte{5, 6})(w, r)
	})
	p := newFor(t, "elevenlabs", srv.URL)
	if _, err := synth(t, p, Request{Text: "hi", Voice: "george", Model: "eleven_flash_v2_5", Speed: 1.1}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[1]
	if r.path != "/v1/text-to-speech/BBBBBBBBBBBBBBBBBBBB/stream" || r.query != "output_format=pcm_24000" {
		t.Errorf("path %q query %q", r.path, r.query)
	}
	if r.header.Get("xi-api-key") != "test-key" {
		t.Error("missing xi-api-key")
	}
	assertBody(t, r.body, map[string]any{"text": "hi", "model_id": "eleven_flash_v2_5", "voice_settings": map[string]any{"speed": 1.1}})

	if _, err := synth(t, p, Request{Text: "hi", Voice: "Nobody"}); err == nil || !strings.Contains(err.Error(), "no voice named") {
		t.Errorf("err = %v", err)
	}
}

func TestGeminiStreaming(t *testing.T) {
	a, b := []byte{1, 2, 3, 4}, []byte{5, 6}
	srv, reqs := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: interaction.created\ndata: {\"event_type\":\"interaction.created\"}\n\n")
		fmt.Fprintf(w, "event: step.start\ndata: {\"index\":0,\"step\":{\"type\":\"model_output\"},\"event_type\":\"step.start\"}\n\n")
		for _, chunk := range [][]byte{a, b} {
			fmt.Fprintf(w, "event: step.delta\ndata: {\"index\":0,\"delta\":{\"type\":\"audio\",\"data\":%q},\"event_type\":\"step.delta\"}\n\n", base64.StdEncoding.EncodeToString(chunk))
			w.(http.Flusher).Flush()
		}
		fmt.Fprintf(w, "event: done\ndata: [DONE]\n\n")
	})
	got, err := synth(t, newFor(t, "gemini", srv.URL), Request{Text: "hi", Voice: "Kore", Model: "gemini-3.8-flash-lite-tts", Instructions: "cheerful"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(a, b...)) {
		t.Errorf("audio = %v", got)
	}
	r := (*reqs)[0]
	if r.path != "/interactions" || r.header.Get("x-goog-api-key") != "test-key" {
		t.Errorf("path %q key %q", r.path, r.header.Get("x-goog-api-key"))
	}
	assertBody(t, r.body, map[string]any{
		"model":  "gemini-3.8-flash-lite-tts",
		"stream": true,
		"input": []any{map[string]any{"type": "user_input", "content": []any{map[string]any{
			"type": "text", "text": "hi",
			"annotations": []any{map[string]any{"type": "speech_metadata", "style": "cheerful"}},
		}}}},
		"response_format":   map[string]any{"type": "audio", "mime_type": "audio/l16", "sample_rate": float64(24000)},
		"generation_config": map[string]any{"speech_config": []any{map[string]any{"voice": "Kore"}}},
	})
}

func TestGeminiStreamErrors(t *testing.T) {
	for name, stream := range map[string]string{
		"error event": "event: error\ndata: {\"error\":{\"message\":\"Deadline expired\",\"code\":\"gateway_timeout\"},\"event_type\":\"error\"}\n\n",
		"no audio":    "event: done\ndata: [DONE]\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := decodeGeminiSSE(strings.NewReader(stream), io.Discard)
			if err == nil {
				t.Fatal("no error")
			}
		})
	}
	// Long lines (big audio deltas) must not trip the line reader.
	big := bytes.Repeat([]byte{7}, 300<<10)
	ev := fmt.Sprintf("data: {\"event_type\":\"step.delta\",\"delta\":{\"type\":\"audio\",\"data\":%q}}\n", base64.StdEncoding.EncodeToString(big))
	var out bytes.Buffer
	if err := decodeGeminiSSE(strings.NewReader(ev), &out); err != nil || !bytes.Equal(out.Bytes(), big) {
		t.Fatalf("big delta: err=%v len=%d", err, out.Len())
	}
}

func TestCartesiaRequest(t *testing.T) {
	srv, reqs := fakeServer(t, pcmHandler([]byte{1, 1}))
	if _, err := synth(t, newFor(t, "cartesia", srv.URL), Request{Text: "hi", Voice: "v-1", Model: "sonic-3.6"}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.path != "/tts/bytes" || r.header.Get("Authorization") != "Bearer test-key" || r.header.Get("Cartesia-Version") == "" {
		t.Errorf("path %q headers %v", r.path, r.header)
	}
	assertBody(t, r.body, map[string]any{
		"model_id": "sonic-3.6", "transcript": "hi", "voice": map[string]any{"id": "v-1"},
		"output_format": map[string]any{"container": "raw", "encoding": "pcm_s16le", "sample_rate": float64(24000)},
	})
}

func TestDeepgramRequest(t *testing.T) {
	srv, reqs := fakeServer(t, pcmHandler([]byte{1, 1}))
	if _, err := synth(t, newFor(t, "deepgram", srv.URL), Request{Text: "hi", Voice: "thalia"}); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.path != "/v1/speak" || r.header.Get("Authorization") != "Token test-key" {
		t.Errorf("path %q auth %q", r.path, r.header.Get("Authorization"))
	}
	if r.query != "container=none&encoding=linear16&model=aura-2-thalia-en&sample_rate=24000" {
		t.Errorf("query %q", r.query)
	}
	assertBody(t, r.body, map[string]any{"text": "hi"})
}

func TestErrorHandling(t *testing.T) {
	cases := map[string]struct {
		handler func(http.ResponseWriter, *http.Request)
		want    string
	}{
		"401 json": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided"}}`)
		}, "HTTP 401: Incorrect API key provided (check the API key"},
		"422 fastapi": {func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"detail":[{"msg":"voice not found"}]}`)
		}, "voice not found"},
		"200 json is not audio": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"detail":{"message":"quota exceeded"}}`)
		}, "quota exceeded"},
		"redirect not followed": {func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
		}, "HTTP 302"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv, reqs := fakeServer(t, c.handler)
			_, err := synth(t, newFor(t, "openai", srv.URL), Request{Text: "x", Voice: "marin", Model: "gpt-4o-mini-tts"})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if strings.Contains(err.Error(), "test-key") {
				t.Fatal("error leaks the API key")
			}
			if len(*reqs) != 1 {
				t.Fatalf("%d requests, want 1", len(*reqs))
			}
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := synth(t, newFor(t, "kokoro", url), Request{Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "nothing is listening") {
		t.Fatalf("err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	srv, _ := fakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		w.Write([]byte{1, 2})
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	w := writerFunc(func(p []byte) (int, error) { cancel(); return len(p), nil })
	err := newFor(t, "openai", srv.URL).Synthesize(ctx, Request{Text: "x"}, w)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func assertBody(t *testing.T, got, want map[string]any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if !bytes.Equal(g, w) {
		t.Errorf("body\n got: %s\nwant: %s", g, w)
	}
}
