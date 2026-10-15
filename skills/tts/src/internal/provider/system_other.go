//go:build !darwin && !linux

package provider

import (
	"context"
	"errors"
	"io"
)

var systemInfo = Info{
	Name:        "system",
	Description: "not available on this OS",
	Local:       true,
	New:         func(Settings) Provider { return unsupportedSystem{} },
}

type unsupportedSystem struct{}

func (unsupportedSystem) Format() Format { return PCM24k }

func (unsupportedSystem) MaxChars() int { return 1 << 30 }

func (unsupportedSystem) Synthesize(context.Context, Request, io.Writer) error {
	return errors.New("system: no built-in speech engine on this OS")
}

// SystemAvailable reports whether the system speech engine is installed.
func SystemAvailable() (string, bool) { return "", false }
