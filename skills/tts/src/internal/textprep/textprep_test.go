package textprep

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"## Summary\n\nAll **tests** pass.", "Summary\n\nAll tests pass."},
		{"See [the docs](https://example.com/docs) now.", "See the docs now."},
		{"Visit https://www.github.com/captjt/skill-tts today", "Visit github.com today"},
		{"Run `go test` first.", "Run go test first."},
		{"Before\n```go\nfunc main() {}\n```\nAfter", "Before\n\nAfter"},
		{"- one\n- two\n* three", "one\ntwo\nthree"},
		{"> quoted *emphasis* here", "quoted emphasis here"},
		{"| a | b |\n|---|---|\n| 1 | 2 |", "a, b.\n\n1, 2."},
		{"2 * 3 * 4 = 24", "2 * 3 * 4 = 24"},
		{"snake_case_name stays", "snake_case_name stays"},
		{"Gemini tags <laugh> survive", "Gemini tags <laugh> survive"},
		{"---\nText", "Text"},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestChunkShortTextIsOneChunk(t *testing.T) {
	if got := Chunk("  Hello there.  ", 100); len(got) != 1 || got[0] != "Hello there." {
		t.Fatalf("got %q", got)
	}
	if got := Chunk("   ", 100); got != nil {
		t.Fatalf("got %q", got)
	}
}

func TestChunkBoundaries(t *testing.T) {
	text := strings.Repeat("This is a sentence. ", 50) + "\n\n" + strings.Repeat("Another one here! ", 40)
	chunks := Chunk(text, 200)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > 200 {
			t.Errorf("chunk of %d bytes exceeds max", len(c))
		}
		if !strings.HasSuffix(c, ".") && !strings.HasSuffix(c, "!") {
			t.Errorf("chunk does not end at a sentence boundary: %q", c[len(c)-20:])
		}
	}
	if got, want := strings.Join(strings.Fields(strings.Join(chunks, " ")), " "), strings.Join(strings.Fields(text), " "); got != want {
		t.Error("chunks do not reproduce the text")
	}
}

func TestChunkHardSplitsUTF8Safely(t *testing.T) {
	text := strings.Repeat("é", 300) // no spaces or punctuation at all
	for _, c := range Chunk(text, 101) {
		if len(c) > 101 || !utf8.ValidString(c) {
			t.Fatalf("bad chunk: %d bytes, valid=%v", len(c), utf8.ValidString(c))
		}
	}
}

func TestChunkFastStart(t *testing.T) {
	text := "This opening sentence is long enough, with a clause or two, that a slow local model would take a while to render it. Then more follows here."
	got := ChunkFastStart(text, 60, 4096)
	if len(got) != 2 || got[0] != "This opening sentence is long enough, with a clause or two," {
		t.Fatalf("got %q", got)
	}
	if strings.Join(got, " ") != text {
		t.Error("fast-start chunks do not reproduce the text")
	}
	if got := ChunkFastStart("Short. Text.", 60, 4096); len(got) != 1 {
		t.Errorf("short text split: %q", got)
	}
	if got := ChunkFastStart(text, 0, 4096); len(got) != 1 {
		t.Errorf("disabled fast start split: %q", got)
	}
}
