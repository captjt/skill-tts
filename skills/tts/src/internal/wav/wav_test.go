package wav

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteThenRead(t *testing.T) {
	pcm := []byte{1, 0, 2, 0, 3, 0, 4, 0}
	path := filepath.Join(t.TempDir(), "a.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Write(f, bytes.NewReader(pcm), 24000, 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	b, _ := os.ReadFile(path)
	if len(b) != 44+len(pcm) || binary.LittleEndian.Uint32(b[40:]) != uint32(len(pcm)) {
		t.Fatalf("bad file: %d bytes", len(b))
	}
	rate, ch, data, err := Data(bytes.NewReader(b))
	if err != nil || rate != 24000 || ch != 1 {
		t.Fatalf("rate=%d ch=%d err=%v", rate, ch, err)
	}
	got, _ := io.ReadAll(data)
	if !bytes.Equal(got, pcm) {
		t.Fatalf("got %v", got)
	}
}

func TestReadSkipsChunksAndStreams(t *testing.T) {
	// macOS `say` inserts a FLLR chunk; pipe writers use placeholder sizes.
	h := Header(0x7ffff000, 22050, 1)
	var b bytes.Buffer
	b.Write(h[:36])
	b.WriteString("FLLR")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	b.Write([]byte{0, 0, 0, 0}) // 3 bytes + pad
	b.Write(h[36:])
	b.Write([]byte{9, 9, 8, 8})
	rate, _, data, err := Data(&b)
	if err != nil || rate != 22050 {
		t.Fatalf("rate=%d err=%v", rate, err)
	}
	got, _ := io.ReadAll(data)
	if !bytes.Equal(got, []byte{9, 9, 8, 8}) {
		t.Fatalf("got %v", got)
	}
}

func TestRejectsNonPCM(t *testing.T) {
	h := Header(4, 24000, 1)
	binary.LittleEndian.PutUint16(h[34:], 24) // 24-bit
	if _, _, _, err := Data(bytes.NewReader(h)); err == nil {
		t.Fatal("accepted 24-bit audio")
	}
}
