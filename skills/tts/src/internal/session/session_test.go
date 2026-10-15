package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleLocksAreRemovedNotSignalled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir, err := runDir()
	if err != nil {
		t.Fatal(err)
	}
	// A lock file nobody holds: its owner exited, and the PID may now belong
	// to an unrelated process, which must not be signalled.
	stale := filepath.Join(dir, "1.lock") // PID 1 must never be signalled
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := StopOthers()
	if err != nil || n != 0 {
		t.Fatalf("stopped %d, err %v", n, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale lock not removed")
	}
}

func TestRegisterSkipsSelf(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	s, err := Register()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := StopOthers(); err != nil || n != 0 {
		t.Fatalf("stopped %d, err %v", n, err)
	}
}
