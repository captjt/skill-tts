package provider

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

type elevenLabs struct {
	baseURL string
	apiKey  string
}

var elevenVoiceID = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)

func (p *elevenLabs) Format() Format { return PCM24k }

func (p *elevenLabs) MaxChars() int { return 5000 }

func (p *elevenLabs) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	voiceID, err := p.resolveVoice(ctx, req.Voice)
	if err != nil {
		return err
	}
	body := map[string]any{"text": req.Text, "model_id": req.Model}
	if req.Speed > 0 {
		body["voice_settings"] = map[string]any{"speed": req.Speed}
	}
	u := fmt.Sprintf("%s/v1/text-to-speech/%s/stream?output_format=pcm_24000", p.baseURL, url.PathEscape(voiceID))
	return stream(ctx, "elevenlabs", u, p.headers(), body, w)
}

func (p *elevenLabs) headers() map[string]string {
	return map[string]string{"xi-api-key": p.apiKey}
}

// resolveVoice accepts a voice ID or a voice name from the account's library.
func (p *elevenLabs) resolveVoice(ctx context.Context, voice string) (string, error) {
	if elevenVoiceID.MatchString(voice) {
		return voice, nil
	}
	voices, err := p.Voices(ctx)
	if err != nil {
		return "", err
	}
	want := strings.ToLower(voice)
	for _, v := range voices { // exact name, then "Name - description" style prefixes
		if strings.ToLower(v.Name) == want {
			return v.ID, nil
		}
	}
	for _, v := range voices {
		if strings.HasPrefix(strings.ToLower(v.Name), want) {
			return v.ID, nil
		}
	}
	return "", fmt.Errorf("elevenlabs: no voice named %q (list them with: tts voices -p elevenlabs)", voice)
}

func (p *elevenLabs) Voices(ctx context.Context) ([]Voice, error) {
	var resp struct {
		Voices []struct {
			ID          string            `json:"voice_id"`
			Name        string            `json:"name"`
			Category    string            `json:"category"`
			Description string            `json:"description"`
			Labels      map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := getJSON(ctx, "elevenlabs", p.baseURL+"/v1/voices", p.headers(), &resp); err != nil {
		return nil, err
	}
	out := make([]Voice, 0, len(resp.Voices))
	for _, v := range resp.Voices {
		var note []string
		for _, k := range []string{"gender", "accent", "age", "description", "use_case"} {
			if l := v.Labels[k]; l != "" {
				note = append(note, l)
			}
		}
		if v.Category != "" {
			note = append(note, v.Category)
		}
		out = append(out, Voice{ID: v.ID, Name: v.Name, Note: strings.Join(note, ", ")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
