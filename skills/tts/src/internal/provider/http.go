package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// UserAgent is sent with every request. Set by main.
var UserAgent = "skill-tts"

// The client has no overall timeout because responses are long-lived audio
// streams; connection setup and time-to-first-header are bounded instead.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second, // local models may load on first request
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   4,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		// Never follow redirects: they could carry the API key header to another host.
		return http.ErrUseLastResponse
	},
}

// APIError is a non-2xx response from a provider.
type APIError struct {
	Provider string
	Status   int
	Message  string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Message)
	switch e.Status {
	case 401, 403:
		msg += " (check the API key: tts auth status)"
	case 429:
		msg += " (rate limited or out of credits)"
	}
	return msg
}

func do(ctx context.Context, provider, method, url string, headers map[string]string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, netError(provider, req.URL.Host, err)
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, decodeError(provider, resp)
	}
	return resp, nil
}

// stream POSTs body and copies the (audio) response to w as it arrives.
func stream(ctx context.Context, provider, url string, headers map[string]string, body any, w io.Writer) error {
	resp, err := do(ctx, provider, http.MethodPost, url, headers, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "text/") {
		// A 200 with a JSON/text body is an error, not audio; don't play it as noise.
		return decodeError(provider, resp)
	}
	_, err = io.CopyBuffer(w, resp.Body, make([]byte, 16<<10))
	return err
}

func getJSON(ctx context.Context, provider, url string, headers map[string]string, out any) error {
	resp, err := do(ctx, provider, http.MethodGet, url, headers, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out)
}

func netError(provider, host string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%s: nothing is listening at %s", provider, host)
	}
	return fmt.Errorf("%s: %w", provider, err)
}

// decodeError extracts a human-readable message from the error shapes used by
// the supported providers.
func decodeError(provider string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := errorMessage(raw)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return &APIError{Provider: provider, Status: resp.StatusCode, Message: msg}
}

func errorMessage(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return truncate(strings.TrimSpace(string(raw)), 300)
	}
	// Probe common shapes: {"error":{"message":..}}, {"detail":{"message":..}},
	// {"detail":".."}, {"message":..}, {"err_msg":..}, [{"error":..}].
	if arr, ok := v.([]any); ok && len(arr) > 0 {
		v = arr[0]
	}
	m, ok := v.(map[string]any)
	if !ok {
		return truncate(string(raw), 300)
	}
	for _, k := range []string{"error", "detail", "message", "err_msg", "title"} {
		switch x := m[k].(type) {
		case string:
			return truncate(x, 300)
		case map[string]any:
			for _, kk := range []string{"message", "msg", "detail", "status"} {
				if s, ok := x[kk].(string); ok {
					return truncate(s, 300)
				}
			}
		case []any:
			if len(x) > 0 {
				if d, ok := x[0].(map[string]any); ok {
					if s, ok := d["msg"].(string); ok {
						return truncate(s, 300)
					}
				}
			}
		}
	}
	return truncate(string(raw), 300)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
