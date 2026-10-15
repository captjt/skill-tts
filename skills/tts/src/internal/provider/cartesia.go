package provider

import (
	"context"
	"encoding/json"
	"io"
	"strings"
)

type cartesia struct {
	baseURL string
	apiKey  string
}

const cartesiaVersion = "2026-08-14"

func (p *cartesia) Format() Format { return PCM24k }

func (p *cartesia) MaxChars() int { return 4000 }

func (p *cartesia) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.apiKey, "Cartesia-Version": cartesiaVersion}
}

func (p *cartesia) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	body := map[string]any{
		"model_id":      req.Model,
		"transcript":    req.Text,
		"voice":         map[string]any{"id": req.Voice},
		"output_format": map[string]any{"container": "raw", "encoding": "pcm_s16le", "sample_rate": 24000},
	}
	if req.Speed > 0 {
		body["generation_config"] = map[string]any{"speed": req.Speed}
	}
	return stream(ctx, "cartesia", p.baseURL+"/tts/bytes", p.headers(), body, w)
}

func (p *cartesia) Voices(ctx context.Context) ([]Voice, error) {
	var raw json.RawMessage
	if err := getJSON(ctx, "cartesia", p.baseURL+"/voices?limit=100", p.headers(), &raw); err != nil {
		return nil, err
	}
	type voice struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Language    string `json:"language"`
	}
	// The voices endpoint has returned both a bare array and {"data": [...]}.
	var list []voice
	if json.Unmarshal(raw, &list) != nil {
		var page struct {
			Data []voice `json:"data"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		list = page.Data
	}
	out := make([]Voice, len(list))
	for i, v := range list {
		out[i] = Voice{ID: v.ID, Name: v.Name, Note: strings.TrimSpace(v.Language + " " + truncate(v.Description, 60))}
	}
	return out, nil
}
