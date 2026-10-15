package provider

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// deepgram selects the voice with the model parameter (e.g. aura-2-thalia-en).
type deepgram struct {
	baseURL string
	apiKey  string
}

var deepgramVoices = []string{
	"thalia", "andromeda", "apollo", "arcas", "aries", "asteria", "athena", "atlas", "aurora",
	"callista", "cora", "cordelia", "delia", "draco", "electra", "harmonia", "helena", "hera",
	"hermes", "hyperion", "iris", "janus", "juno", "jupiter", "luna", "mars", "minerva", "neptune",
	"odysseus", "ophelia", "orion", "orpheus", "pandora", "phoebe", "pluto", "saturn", "selene",
	"theia", "vesta", "zeus",
}

func (p *deepgram) Format() Format { return PCM24k }

func (p *deepgram) MaxChars() int { return 2000 }

func (p *deepgram) Synthesize(ctx context.Context, req Request, w io.Writer) error {
	q := url.Values{
		"model":       {deepgramModel(req.Voice)},
		"encoding":    {"linear16"},
		"sample_rate": {"24000"},
		"container":   {"none"},
	}
	if req.Speed > 0 {
		q.Set("speed", strconv.FormatFloat(req.Speed, 'f', -1, 64))
	}
	headers := map[string]string{"Authorization": "Token " + p.apiKey}
	return stream(ctx, "deepgram", fmt.Sprintf("%s/v1/speak?%s", p.baseURL, q.Encode()), headers, map[string]any{"text": req.Text}, w)
}

// deepgramModel accepts "thalia" as shorthand for "aura-2-thalia-en".
func deepgramModel(voice string) string {
	if strings.HasPrefix(voice, "aura") {
		return voice
	}
	return "aura-2-" + strings.ToLower(voice) + "-en"
}

func (p *deepgram) Voices(context.Context) ([]Voice, error) {
	out := make([]Voice, len(deepgramVoices))
	for i, v := range deepgramVoices {
		out[i] = Voice{ID: deepgramModel(v), Name: v, Note: "English"}
	}
	return out, nil
}
