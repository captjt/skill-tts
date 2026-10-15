// Package provider implements text-to-speech backends. Every backend produces
// raw 16-bit signed little-endian PCM so audio can be streamed straight to the
// speaker as it arrives, without decoding.
package provider

import (
	"context"
	"io"
	"slices"
)

// Format describes a raw PCM stream (always 16-bit signed little-endian).
type Format struct {
	SampleRate int
	Channels   int
}

// PCM24k is mono 24 kHz, the native rate of every cloud provider supported here.
var PCM24k = Format{SampleRate: 24000, Channels: 1}

// Request is a single synthesis request.
type Request struct {
	Text         string
	Voice        string
	Model        string
	Speed        float64 // 0 means the provider default
	Instructions string  // delivery/style prompt, for providers that support one
}

// Provider synthesizes speech as a PCM stream.
type Provider interface {
	Format() Format
	// MaxChars is the longest text sent in one request; longer text is split.
	MaxChars() int
	// Synthesize writes PCM for req to w as it is generated.
	Synthesize(ctx context.Context, req Request, w io.Writer) error
}

// DirectSpeaker is implemented by providers that play audio themselves
// (e.g. macOS `say`), which is faster than rendering PCM first.
type DirectSpeaker interface {
	// Speak plays req and blocks until done. started is called once audio
	// output has begun.
	Speak(ctx context.Context, req Request, started func()) error
}

// Voice is one entry returned by a VoiceLister.
type Voice struct {
	ID   string
	Name string
	Note string
}

// VoiceLister is implemented by providers that can enumerate their voices.
type VoiceLister interface {
	Voices(ctx context.Context) ([]Voice, error)
}

// Settings are the per-user values needed to construct a provider.
type Settings struct {
	APIKey  string
	BaseURL string // empty means the provider default
}

// Info describes a provider and how to construct it.
type Info struct {
	Name         string
	Description  string
	KeyEnv       []string // env vars checked for an API key; empty means none needed
	Local        bool     // text and audio never leave this machine
	DefaultVoice string
	DefaultModel string
	RenderModel  string // higher-fidelity model used by `tts render`, if different
	BaseURLEnv   string // env var that overrides the base URL, if supported
	// FastStart, if set, caps the first chunk (bytes) for providers that render
	// a whole chunk before streaming, so playback starts sooner.
	FastStart int
	New       func(Settings) Provider
}

// NeedsKey reports whether the provider requires an API key.
func (i Info) NeedsKey() bool { return len(i.KeyEnv) > 0 }

var registry = []Info{
	systemInfo,
	{
		Name:         "openai",
		Description:  "OpenAI gpt-4o-mini-tts (steerable with --instructions)",
		KeyEnv:       []string{"OPENAI_API_KEY"},
		DefaultVoice: "marin",
		DefaultModel: "gpt-4o-mini-tts",
		BaseURLEnv:   "TTS_OPENAI_BASE_URL",
		New: func(s Settings) Provider {
			return &openAI{name: "openai", baseURL: or(s.BaseURL, "https://api.openai.com/v1"), apiKey: s.APIKey}
		},
	},
	{
		Name:         "elevenlabs",
		Description:  "ElevenLabs (Flash v2.5 by default, ~75ms model latency)",
		KeyEnv:       []string{"ELEVENLABS_API_KEY", "ELEVEN_API_KEY"},
		DefaultVoice: "JBFqnCBsd6RMkjVDRZzb", // "George"
		DefaultModel: "eleven_flash_v2_5",
		RenderModel:  "eleven_multilingual_v2",
		New: func(s Settings) Provider {
			return &elevenLabs{baseURL: or(s.BaseURL, "https://api.elevenlabs.io"), apiKey: s.APIKey}
		},
	},
	{
		Name:         "gemini",
		Description:  "Google Gemini TTS (style via --instructions)",
		KeyEnv:       []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		DefaultVoice: "Kore",
		DefaultModel: "gemini-3.8-flash-lite-tts",
		RenderModel:  "gemini-3.8-flash-tts",
		New: func(s Settings) Provider {
			return &gemini{baseURL: or(s.BaseURL, "https://generativelanguage.googleapis.com/v1beta"), apiKey: s.APIKey}
		},
	},
	{
		Name:         "cartesia",
		Description:  "Cartesia Sonic (low-latency streaming)",
		KeyEnv:       []string{"CARTESIA_API_KEY"},
		DefaultVoice: "db6b0ed5-d5d3-463d-ae85-518a07d3c2b4",
		DefaultModel: "sonic-3.6",
		New: func(s Settings) Provider {
			return &cartesia{baseURL: or(s.BaseURL, "https://api.cartesia.ai"), apiKey: s.APIKey}
		},
	},
	{
		Name:         "deepgram",
		Description:  "Deepgram Aura-2",
		KeyEnv:       []string{"DEEPGRAM_API_KEY"},
		DefaultVoice: "aura-2-thalia-en",
		New: func(s Settings) Provider {
			return &deepgram{baseURL: or(s.BaseURL, "https://api.deepgram.com"), apiKey: s.APIKey}
		},
	},
	{
		Name:         "kokoro",
		Description:  "Kokoro-82M via a local OpenAI-compatible server (e.g. Kokoro-FastAPI)",
		Local:        true,
		DefaultVoice: "af_heart",
		DefaultModel: "kokoro",
		BaseURLEnv:   "KOKORO_BASE_URL",
		FastStart:    60,
		New: func(s Settings) Provider {
			return &openAI{name: "kokoro", baseURL: or(s.BaseURL, KokoroDefaultURL), apiKey: s.APIKey, local: true}
		},
	},
}

// KokoroDefaultURL is where Kokoro-FastAPI listens by default.
const KokoroDefaultURL = "http://localhost:8880/v1"

// All returns every registered provider.
func All() []Info { return slices.Clone(registry) }

// Lookup returns the provider with the given name.
func Lookup(name string) (Info, bool) {
	for _, i := range registry {
		if i.Name == name {
			return i, true
		}
	}
	return Info{}, false
}

// Names returns the registered provider names.
func Names() []string {
	names := make([]string, len(registry))
	for i, info := range registry {
		names[i] = info.Name
	}
	return names
}

func or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
