// Package wav reads and writes 16-bit PCM WAV files.
package wav

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Header returns a canonical 44-byte header for dataLen bytes of s16le PCM.
func Header(dataLen uint32, sampleRate, channels int) []byte {
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], 36+dataLen)
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(h[28:], uint32(sampleRate*channels*2))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*2))
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], dataLen)
	return h
}

// Write streams pcm into w as a WAV file, then patches the sizes in the header.
func Write(w io.WriteSeeker, pcm io.Reader, sampleRate, channels int) (int64, error) {
	if _, err := w.Write(Header(0, sampleRate, channels)); err != nil {
		return 0, err
	}
	n, err := io.Copy(w, pcm)
	if err != nil {
		return n, err
	}
	if n > 0xFFFFFFFF-36 {
		return n, errors.New("wav: audio too long for a WAV file")
	}
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return n, err
	}
	if _, err := w.Write(Header(uint32(n), sampleRate, channels)); err != nil {
		return n, err
	}
	_, err = w.Seek(0, io.SeekEnd)
	return n, err
}

// Data parses a WAV header and returns the format and a reader positioned at
// the PCM samples. Streams with an unknown length (size 0 or 0xFFFFFFFF, as
// written by tools that output to a pipe) are read to EOF.
func Data(r io.Reader) (sampleRate, channels int, data io.Reader, err error) {
	var riff [12]byte
	if _, err = io.ReadFull(r, riff[:]); err != nil {
		return 0, 0, nil, fmt.Errorf("wav: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return 0, 0, nil, errors.New("wav: not a RIFF/WAVE stream")
	}
	gotFmt := false
	for {
		var ch [8]byte
		if _, err = io.ReadFull(r, ch[:]); err != nil {
			return 0, 0, nil, fmt.Errorf("wav: no data chunk: %w", err)
		}
		id, size := string(ch[0:4]), binary.LittleEndian.Uint32(ch[4:])
		switch id {
		case "fmt ":
			if size < 16 || size > 1024 {
				return 0, 0, nil, errors.New("wav: bad fmt chunk")
			}
			buf := make([]byte, size+size%2)
			if _, err = io.ReadFull(r, buf); err != nil {
				return 0, 0, nil, fmt.Errorf("wav: %w", err)
			}
			tag := binary.LittleEndian.Uint16(buf[0:])
			channels = int(binary.LittleEndian.Uint16(buf[2:]))
			sampleRate = int(binary.LittleEndian.Uint32(buf[4:]))
			bits := binary.LittleEndian.Uint16(buf[14:])
			if (tag != 1 && tag != 0xFFFE) || bits != 16 {
				return 0, 0, nil, fmt.Errorf("wav: unsupported encoding (format %d, %d-bit)", tag, bits)
			}
			gotFmt = true
		case "data":
			if !gotFmt {
				return 0, 0, nil, errors.New("wav: data chunk before fmt chunk")
			}
			if size == 0 || size >= 0x7F000000 { // placeholder sizes from pipe writers

				return sampleRate, channels, r, nil
			}
			return sampleRate, channels, io.LimitReader(r, int64(size)), nil
		default:
			if _, err = io.CopyN(io.Discard, r, int64(size)+int64(size%2)); err != nil {
				return 0, 0, nil, fmt.Errorf("wav: %w", err)
			}
		}
	}
}
