// Package pipeline streams multi-chunk synthesis as one ordered PCM stream.
//
// Chunk i streams straight to the reader as it arrives; chunk i+1 is requested
// as soon as chunk i starts being consumed, so long text plays without gaps
// while never running more than one request ahead of playback.
package pipeline

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/captjt/skill-tts/internal/provider"
)

// Stream is an io.Reader over the concatenated PCM of all chunks.
type Stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	prov   provider.Provider
	base   provider.Request
	chunks []string

	bufs    []*buffer
	cur     int
	carry   []byte // odd trailing byte held back to keep 16-bit samples aligned
	start   time.Time
	first   chan struct{}
	firstAt time.Time
	once    sync.Once
}

// New starts synthesizing chunk 0 immediately.
func New(ctx context.Context, p provider.Provider, base provider.Request, chunks []string) *Stream {
	ctx, cancel := context.WithCancel(ctx)
	s := &Stream{
		ctx: ctx, cancel: cancel, prov: p, base: base, chunks: chunks,
		bufs:  make([]*buffer, len(chunks)),
		start: time.Now(),
		first: make(chan struct{}),
	}
	if len(chunks) > 0 {
		s.fetch(0)
	} else {
		close(s.first)
	}
	return s
}

func (s *Stream) fetch(i int) {
	if i >= len(s.chunks) || s.bufs[i] != nil {
		return
	}
	b := newBuffer()
	s.bufs[i] = b
	req := s.base
	req.Text = s.chunks[i]
	go func() {
		err := s.prov.Synthesize(s.ctx, req, b)
		b.finish(err)
	}()
	if i == 0 {
		go func() {
			b.waitData()
			s.markFirst()
		}()
	}
}

func (s *Stream) markFirst() {
	s.once.Do(func() {
		s.firstAt = time.Now()
		close(s.first)
	})
}

// WaitFirst blocks until the first audio bytes arrive (or chunk 0 fails),
// returning the time to first audio.
func (s *Stream) WaitFirst() (time.Duration, error) {
	select {
	case <-s.first:
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	}
	if len(s.bufs) > 0 {
		if err := s.bufs[0].earlyErr(); err != nil {
			return 0, err
		}
	}
	return s.firstAt.Sub(s.start), nil
}

func (s *Stream) Read(p []byte) (int, error) {
	if len(p) < 2 {
		return 0, io.ErrShortBuffer
	}
	for s.cur < len(s.chunks) {
		b := s.bufs[s.cur]
		s.fetch(s.cur + 1) // look one chunk ahead while this one plays
		n := copy(p, s.carry)
		s.carry = s.carry[:0]
		m, err := b.read(p[n:])
		n += m
		if n > 0 {
			if n%2 == 1 { // hold back half a sample until the next read
				s.carry = append(s.carry, p[n-1])
				n--
			}
			if n > 0 {
				return n, nil
			}
		}
		if err == io.EOF {
			s.cur++
			continue
		}
		if err != nil {
			s.cancel()
			return 0, err
		}
	}
	return 0, io.EOF
}

// Close cancels any in-flight requests.
func (s *Stream) Close() error {
	s.cancel()
	return nil
}

// buffer is an unbounded in-memory pipe: the producer never blocks (audio is
// ~48 KB/s, so memory is not a concern) and the reader waits for data.
type buffer struct {
	mu      sync.Mutex
	cond    *sync.Cond
	data    []byte
	done    bool
	err     error
	written int
}

func newBuffer() *buffer {
	b := &buffer{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	b.written += len(p)
	b.cond.Broadcast()
	return len(p), nil
}

func (b *buffer) finish(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done, b.err = true, err
	b.cond.Broadcast()
}

// waitData blocks until data is available or the producer finished.
func (b *buffer) waitData() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.data) == 0 && !b.done {
		b.cond.Wait()
	}
}

// earlyErr returns the producer error if it failed before writing anything.
func (b *buffer) earlyErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done && b.written == 0 {
		if b.err != nil {
			return b.err
		}
		return io.ErrUnexpectedEOF
	}
	return nil
}

func (b *buffer) read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.data) == 0 && !b.done {
		b.cond.Wait()
	}
	if len(b.data) > 0 {
		n := copy(p, b.data)
		b.data = b.data[n:]
		if len(b.data) == 0 {
			b.data = nil // let the backing array go
		}
		return n, nil
	}
	if b.err != nil {
		return 0, b.err
	}
	return 0, io.EOF
}
