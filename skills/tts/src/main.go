// Command tts speaks text aloud (or writes audio files) using a choice of
// local and cloud text-to-speech providers. It is the engine behind the
// skill-tts agent skill.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/captjt/skill-tts/internal/provider"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `tts - text to speech for coding agents

Usage:
  tts [speak] [flags] [text...]   speak text from args or stdin; returns once
                                  audio starts, playback continues in background
  tts render SCRIPT.json [-o DIR] render a multi-segment voiceover with timings
                                  (see: tts render --help)
  tts stop                        stop all speech (playing and queued)
  tts voices [-p provider] [filter]
  tts providers                   list providers and whether they're ready
  tts config [show | get KEY | set KEY VALUE | unset KEY | path]
  tts auth [status | set PROVIDER | delete PROVIDER]
  tts doctor                      check the setup
  tts version

Speak flags:
  -p, --provider NAME    ` + "system, openai, elevenlabs, gemini, cartesia, deepgram, kokoro" + `
  -v, --voice VOICE      voice name or ID (see: tts voices)
  -m, --model MODEL      provider model
  -s, --speed N          0.25-4, 1 = normal
  -i, --instructions S   delivery style, e.g. "calm and warm" (openai, gemini)
  -o, --out FILE         write audio to FILE instead of playing (.wav; other
                         extensions are converted with ffmpeg or afconvert)
  -w, --wait             block until playback finishes
  -q, --queue            play after current speech instead of interrupting it
      --raw              speak text as-is (default strips Markdown and code)
      --max-chars N      refuse longer text for paid providers (default 20000)

Configuration: ~/.config/skill-tts/config.json (see: tts config).
API keys come from env vars (OPENAI_API_KEY, ELEVENLABS_API_KEY, GEMINI_API_KEY,
CARTESIA_API_KEY, DEEPGRAM_API_KEY) or the OS keychain (see: tts auth).
`

func main() {
	provider.UserAgent = "skill-tts/" + version
	os.Exit(run(os.Args[1:]))
}

var commands = map[string]func([]string) error{
	"speak":     cmdSpeak,
	"render":    cmdRender,
	"stop":      cmdStop,
	"voices":    cmdVoices,
	"providers": cmdProviders,
	"config":    cmdConfig,
	"auth":      cmdAuth,
	"doctor":    cmdDoctor,
	"version":   cmdVersion,
	"__worker":  cmdWorker,
}

func run(args []string) int {
	name := "speak"
	if len(args) > 0 {
		switch a := args[0]; {
		case a == "help" || a == "-h" || a == "--help":
			fmt.Print(usage)
			return 0
		case a == "--version":
			name, args = "version", nil
		case a == "say":
			name, args = "speak", args[1:]
		case commands[a] != nil:
			name, args = a, args[1:]
		}
	}
	if err := commands[name](args); err != nil {
		fmt.Fprintln(os.Stderr, "tts:", err)
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintln(os.Stderr, "Run 'tts help' for usage.")
			return 2
		}
		return 1
	}
	return 0
}

type usageError struct{ error }

func usagef(format string, a ...any) error { return usageError{fmt.Errorf(format, a...)} }

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseArgs parses flags that may appear before, between, or after positional
// arguments. Everything after a literal "--" is positional.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for i, a := range args {
		if a == "--" {
			args, rest = args[:i], args[i+1:]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Print(usage)
				os.Exit(0)
			}
			return nil, usageError{err}
		}
		args = fs.Args()
		if len(args) == 0 {
			return append(pos, rest...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// stringFlag registers the same string flag under a short and a long name.
func stringFlag(fs *flag.FlagSet, p *string, short, long string) {
	fs.StringVar(p, short, "", "")
	fs.StringVar(p, long, "", "")
}

func boolFlag(fs *flag.FlagSet, p *bool, short, long string) {
	if short != "" {
		fs.BoolVar(p, short, false, "")
	}
	fs.BoolVar(p, long, false, "")
}

func lookupProvider(name string) (provider.Info, error) {
	info, ok := provider.Lookup(name)
	if !ok {
		return info, usagef("unknown provider %q (choose from: %s)", name, strings.Join(provider.Names(), ", "))
	}
	return info, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
