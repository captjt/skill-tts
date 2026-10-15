package provider

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

// openAI speaks the OpenAI /audio/speech protocol. It also backs "kokoro",
// since Kokoro-FastAPI (and most local TTS servers) implement the same API.
type openAI struct {
	name    string
	baseURL string
	apiKey  string
	local   bool
}

var openAIVoices = []string{"marin", "cedar", "alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse"}

func (p *openAI) Format() Format { return PCM24k }

func (p *openAI) MaxChars() int { return 4096 }

func (p *openAI) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	body := map[string]any{
		"model":           req.Model,
		"input":           req.Text,
		"voice":           req.Voice,
		"response_format": "pcm", // 24 kHz s16le mono, no header
	}
	if req.Speed > 0 {
		body["speed"] = req.Speed
	}
	// Only gpt-4o-mini-tts style models accept instructions; tts-1 rejects them.
	if req.Instructions != "" && !p.local && strings.HasPrefix(req.Model, "gpt-") {
		body["instructions"] = req.Instructions
	}
	return stream(ctx, p.name, strings.TrimRight(p.baseURL, "/")+"/audio/speech", p.headers(), body, w)
}

func (p *openAI) headers() map[string]string {
	if p.apiKey == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + p.apiKey}
}

func (p *openAI) Voices(ctx context.Context) ([]Voice, error) {
	if !p.local {
		out := make([]Voice, len(openAIVoices))
		for i, v := range openAIVoices {
			out[i] = Voice{ID: v, Name: v}
			if v == "marin" || v == "cedar" {
				out[i].Note = "recommended"
			}
		}
		return out, nil
	}
	// Kokoro-FastAPI: GET /audio/voices -> {"voices": [...]}, where entries
	// are strings in older releases and objects in newer ones.
	var resp struct {
		Voices []json.RawMessage `json:"voices"`
	}
	if err := getJSON(ctx, p.name, strings.TrimRight(p.baseURL, "/")+"/audio/voices", p.headers(), &resp); err != nil {
		return nil, err
	}
	out := make([]Voice, 0, len(resp.Voices))
	for _, raw := range resp.Voices {
		var v struct {
			ID    string `json:"id"`
			Grade string `json:"overall_grade"`
		}
		if json.Unmarshal(raw, &v.ID) != nil && json.Unmarshal(raw, &v) != nil || v.ID == "" {
			continue
		}
		note := kokoroVoiceNote(v.ID)
		if v.Grade != "" {
			note += ", grade " + v.Grade
		}
		out = append(out, Voice{ID: v.ID, Name: v.ID, Note: note})
	}
	return out, nil
}

// kokoroVoiceNote decodes Kokoro's voice-name prefix (e.g. "af" = American female).
func kokoroVoiceNote(id string) string {
	if len(id) < 3 || id[2] != '_' {
		return ""
	}
	lang := map[byte]string{'a': "American English", 'b': "British English", 'e': "Spanish", 'f': "French", 'h': "Hindi", 'i': "Italian", 'j': "Japanese", 'p': "Portuguese", 'z': "Mandarin"}[id[0]]
	sex := map[byte]string{'f': "female", 'm': "male"}[id[1]]
	return strings.TrimSpace(lang + " " + sex)
}
