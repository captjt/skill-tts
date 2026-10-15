package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/captjt/skill-tts/internal/provider"
)

// fake emits each chunk's text as audio bytes, in small odd-sized writes.
type fake struct {
	mu       sync.Mutex
	inFlight int32
	maxSeen  int32
	started  []string
	failOn   string
	delay    time.Duration
}

func (f *fake) Format() provider.Format { return provider.PCM24k }
func (f *fake) MaxChars() int           { return 1 << 20 }

func (f *fake) Synthesize(ctx context.Context, req provider.Request, w io.Writer) error {
	n := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	f.mu.Lock()
	f.started = append(f.started, req.Text)
	if n > f.maxSeen {
		f.maxSeen = n
	}
	f.mu.Unlock()
	if req.Text == f.failOn {
		return errors.New("boom")
	}
	data := []byte(req.Text)
	for len(data) > 0 {
		k := min(3, len(data))
		if _, err := w.Write(data[:k]); err != nil {
			return err
		}
		data = data[k:]
		time.Sleep(f.delay)
	}
	return ctx.Err()
}

func TestOrderedAndAligned(t *testing.T) {
	f := &fake{delay: time.Millisecond}
	chunks := []string{"aaaa", "bbbbbb", "cc", "dddddddd"}
	s := New(context.Background(), f, provider.Request{}, chunks)
	if _, err := s.WaitFirst(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	buf := make([]byte, 5) // odd buffer to exercise sample alignment
	for {
		n, err := s.Read(buf)
		if n%2 != 0 {
			t.Fatalf("read %d bytes: not sample aligned", n)
		}
		out.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := out.String(); got != strings.Join(chunks, "") {
		t.Errorf("got %q", got)
	}
	if f.maxSeen > 2 {
		t.Errorf("%d requests in flight; lookahead should be 1", f.maxSeen)
	}
}

func TestLookaheadIsBounded(t *testing.T) {
	f := &fake{}
	s := New(context.Background(), f, provider.Request{}, []string{"aa", "bb", "cc", "dd"})
	defer s.Close()
	if _, err := s.WaitFirst(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	n := len(f.started)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d chunks requested before playback started; want 1", n)
	}
	buf := make([]byte, 2)
	s.Read(buf) // consuming chunk 0 may request chunk 1, no more
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	n = len(f.started)
	f.mu.Unlock()
	if n != 2 {
		t.Fatalf("%d chunks requested while playing chunk 0; want 2", n)
	}
}

func TestFirstChunkError(t *testing.T) {
	f := &fake{failOn: "bad"}
	s := New(context.Background(), f, provider.Request{}, []string{"bad", "ok"})
	if _, err := s.WaitFirst(); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestLaterChunkError(t *testing.T) {
	f := &fake{failOn: "bad"}
	s := New(context.Background(), f, provider.Request{}, []string{"good", "bad"})
	if _, err := s.WaitFirst(); err != nil {
		t.Fatal(err)
	}
	_, err := io.ReadAll(s)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
}
