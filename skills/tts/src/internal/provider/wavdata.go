package provider

import (
	"fmt"
	"io"

	"github.com/captjt/skill-tts/internal/wav"
)

// copyWAVData copies the PCM samples of a WAV stream to w, checking the format.
func copyWAVData(r io.Reader, w io.Writer, want Format) error {
	rate, channels, data, err := wav.Data(r)
	if err != nil {
		return err
	}
	if rate != want.SampleRate || channels != want.Channels {
		return fmt.Errorf("unexpected audio format: %d Hz, %d channel(s)", rate, channels)
	}
	_, err = io.Copy(w, data)
	return err
}
