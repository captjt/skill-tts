package wav

import "time"

// TrimSilence removes leading and trailing near-silence from s16le PCM,
// keeping pad of audio around the speech so words aren't clipped. Providers
// pad their output inconsistently; trimming makes segment durations match
// what is actually spoken, which matters when timing narration to video.
func TrimSilence(pcm []byte, sampleRate, channels int, pad time.Duration) []byte {
	const threshold = 328 // about -40 dBFS
	frame := 2 * channels
	frames := len(pcm) / frame
	loud := func(i int) bool {
		for c := range channels {
			o := i*frame + 2*c
			v := int16(uint16(pcm[o]) | uint16(pcm[o+1])<<8)
			if v > threshold || v < -threshold {
				return true
			}
		}
		return false
	}
	first, last := -1, -1
	for i := range frames {
		if loud(i) {
			first = i
			break
		}
	}
	if first < 0 {
		return pcm[:0] // all silence
	}
	for i := frames - 1; i >= first; i-- {
		if loud(i) {
			last = i
			break
		}
	}
	padFrames := int(pad.Seconds() * float64(sampleRate))
	start := max(0, first-padFrames)
	end := min(frames, last+1+padFrames)
	return pcm[start*frame : end*frame]
}
