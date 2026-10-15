package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// gemini uses the Interactions API with "stream": true, which emits SSE
// events whose step.delta payloads carry base64 raw PCM (audio/l16, 24 kHz).
type gemini struct {
	baseURL string
	apiKey  string
}

var geminiVoices = []string{
	"Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe",
	"Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina", "Erinome", "Algenib", "Rasalgethi",
	"Laomedeia", "Achernar", "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird",
	"Zubenelgenubi", "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat",
}

func (p *gemini) Format() Format { return PCM24k }

func (p *gemini) MaxChars() int { return 4000 }

func (p *gemini) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	text := map[string]any{"type": "text", "text": req.Text}
	if req.Instructions != "" {
		text["annotations"] = []any{map[string]any{"type": "speech_metadata", "style": req.Instructions}}
	}
	body := map[string]any{
		"model":             req.Model,
		"input":             []any{map[string]any{"type": "user_input", "content": []any{text}}},
		"response_format":   map[string]any{"type": "audio", "mime_type": "audio/l16", "sample_rate": 24000},
		"generation_config": map[string]any{"speech_config": []any{map[string]any{"voice": req.Voice}}},
		"stream":            true,
	}
	resp, err := do(ctx, "gemini", http.MethodPost, p.baseURL+"/interactions",
		map[string]string{"x-goog-api-key": p.apiKey, "Accept": "text/event-stream"}, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeGeminiSSE(resp.Body, w)
}

type geminiEvent struct {
	EventType string `json:"event_type"`
	Delta     *struct {
		Type string `json:"type"`
		Data string `json:"data"`
	} `json:"delta"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// decodeGeminiSSE writes the audio carried by an Interactions SSE stream to w.
func decodeGeminiSSE(r io.Reader, w io.Writer) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	wrote := false
	for {
		// Audio deltas can be far longer than a bufio buffer; accumulate.
		part, isPrefix, err := br.ReadLine()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("gemini: %w", err)
		}
		line = append(line, part...)
		if isPrefix {
			continue
		}
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		line = line[:0]
		if !ok {
			continue // "event:" lines, comments, blank separators
		}
		data = bytes.TrimSpace(data)
		if string(data) == "[DONE]" {
			break
		}
		var ev geminiEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return fmt.Errorf("gemini: bad stream event: %w", err)
		}
		if ev.Error != nil {
			return fmt.Errorf("gemini: %s (%s)", ev.Error.Message, ev.Error.Code)
		}
		if ev.EventType != "step.delta" || ev.Delta == nil || ev.Delta.Type != "audio" {
			continue
		}
		pcm, err := base64.StdEncoding.DecodeString(ev.Delta.Data)
		if err != nil {
			return fmt.Errorf("gemini: bad audio chunk: %w", err)
		}
		if _, err := w.Write(pcm); err != nil {
			return err
		}
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("gemini: response contained no audio")
	}
	return nil
}

func (p *gemini) Voices(context.Context) ([]Voice, error) {
	out := make([]Voice, len(geminiVoices))
	for i, v := range geminiVoices {
		out[i] = Voice{ID: v, Name: v}
	}
	return out, nil
}
