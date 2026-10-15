//go:build !linux

package audio

func deviceCheck() error { return nil }
