// Package session coordinates concurrent tts processes: one voice at a time,
// `tts stop`, and queued speech.
//
// Every speaking process holds an exclusive flock on run/<pid>.lock for its
// lifetime. A lock file we can lock ourselves is stale, so a recycled PID is
// never signalled. Playback itself is serialized by a flock on run/play.lock.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Dir is where runtime state (locks, the worker log) lives.
func Dir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		if d, err := os.UserCacheDir(); err == nil {
			base = d
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "skill-tts")
}

func runDir() (string, error) {
	d := filepath.Join(Dir(), "run")
	return d, os.MkdirAll(d, 0o700)
}

// LogPath is where detached workers write errors.
func LogPath() string { return filepath.Join(Dir(), "last-error.log") }

// Session marks this process as an active speaker until Close.
type Session struct {
	lock *os.File
	play *os.File
}

// Register creates and locks run/<pid>.lock.
func Register() (*Session, error) {
	dir, err := runDir()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return &Session{lock: f}, nil
}

// AcquirePlayback blocks until no other process is playing.
func (s *Session) AcquirePlayback(ctx context.Context) error {
	dir, err := runDir()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "play.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			s.play = f
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// Close releases the locks.
func (s *Session) Close() {
	if s.play != nil {
		s.play.Close()
	}
	name := s.lock.Name()
	os.Remove(name)
	s.lock.Close()
}

// StopOthers signals every other live speaker (playing or queued) to stop and
// returns how many were stopped.
func StopOthers() (int, error) {
	dir, err := runDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	self := os.Getpid()
	stopped := 0
	for _, e := range entries {
		pidStr, ok := strings.CutSuffix(e.Name(), ".lock")
		pid, err := strconv.Atoi(pidStr)
		if !ok || err != nil || pid == self {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			os.Remove(path) // stale: its owner is gone
			f.Close()
			continue
		}
		f.Close()
		// Workers lead their own process group, so this also stops any
		// child player; fall back to the PID for foreground (--wait) runs.
		if syscall.Kill(-pid, syscall.SIGTERM) != nil {
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return stopped, fmt.Errorf("stopping %d: %w", pid, err)
			}
		}
		stopped++
	}
	return stopped, nil
}
