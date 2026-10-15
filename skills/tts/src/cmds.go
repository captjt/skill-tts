package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/captjt/skill-tts/internal/audio"
	"github.com/captjt/skill-tts/internal/config"
	"github.com/captjt/skill-tts/internal/provider"
	"github.com/captjt/skill-tts/internal/secrets"
	"github.com/captjt/skill-tts/internal/session"
)

const kokoroDocker = "docker run -d --name kokoro -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:v0.9.0"

func cmdVersion([]string) error {
	fmt.Printf("tts %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}

func cmdStop([]string) error {
	n, err := session.StopOthers()
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Println("Nothing is playing.")
	} else {
		fmt.Printf("Stopped %d speech process(es).\n", n)
	}
	return nil
}

func cmdVoices(args []string) error {
	var name string
	fs := newFlags("voices")
	stringFlag(fs, &name, "p", "provider")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	name = first(name, os.Getenv("TTS_PROVIDER"), cfg.Provider, "system")
	info, err := lookupProvider(name)
	if err != nil {
		return err
	}
	prov, err := newProvider(info, cfg)
	if err != nil {
		return err
	}
	lister, ok := prov.(provider.VoiceLister)
	if !ok {
		return fmt.Errorf("%s can't list voices", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	voices, err := lister.Voices(ctx)
	if err != nil {
		return hintLocal(info, err)
	}
	filter := strings.ToLower(strings.Join(pos, " "))
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	shown := 0
	for _, v := range voices {
		line := v.ID
		if v.Name != v.ID {
			line += "\t" + v.Name
		}
		if v.Note != "" {
			line += "\t" + v.Note
		}
		if filter != "" && !strings.Contains(strings.ToLower(line), filter) {
			continue
		}
		fmt.Fprintln(tw, line)
		shown++
	}
	tw.Flush()
	if shown == 0 {
		fmt.Println("No matching voices.")
	}
	if info.DefaultVoice != "" {
		fmt.Printf("\nDefault: %s. Choose one with --voice, or: tts config set %s.voice VOICE\n", first(cfg.For(name).Voice, info.DefaultVoice), name)
	}
	return nil
}

func newProvider(info provider.Info, cfg config.Config) (provider.Provider, error) {
	s := provider.Settings{BaseURL: first(envOrEmpty(info.BaseURLEnv), cfg.For(info.Name).BaseURL)}
	if info.NeedsKey() {
		s.APIKey, _ = secrets.Lookup(info.Name, info.KeyEnv)
		if s.APIKey == "" {
			return nil, fmt.Errorf("no API key for %s: set %s, or run `tts auth set %s` in a terminal", info.Name, info.KeyEnv[0], info.Name)
		}
	}
	return info.New(s), nil
}

func envOrEmpty(name string) string {
	if name == "" {
		return ""
	}
	return os.Getenv(name)
}

func hintLocal(info provider.Info, err error) error {
	if info.Name == "kokoro" && strings.Contains(err.Error(), "nothing is listening") {
		return fmt.Errorf("%w\nStart Kokoro-FastAPI with:\n  %s", err, kokoroDocker)
	}
	return err
}

// status reports whether a provider is usable right now.
func status(info provider.Info, cfg config.Config) (ok bool, detail string) {
	switch {
	case info.Name == "system":
		if bin, found := provider.SystemAvailable(); found {
			return true, bin
		}
		return false, "install espeak-ng"
	case info.NeedsKey():
		_, src := secrets.Lookup(info.Name, info.KeyEnv)
		if src == "" {
			return false, fmt.Sprintf("no key: set %s or run `tts auth set %s`", info.KeyEnv[0], info.Name)
		}
		return true, "key from " + src
	default: // local server
		prov, _ := newProvider(info, cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		base := first(envOrEmpty(info.BaseURLEnv), cfg.For(info.Name).BaseURL, provider.KokoroDefaultURL)
		voices, err := prov.(provider.VoiceLister).Voices(ctx)
		if err != nil {
			return false, fmt.Sprintf("not reachable at %s (start: %s)", base, kokoroDocker)
		}
		return true, fmt.Sprintf("running at %s, %d voices", base, len(voices))
	}
}

func cmdProviders([]string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	def := first(os.Getenv("TTS_PROVIDER"), cfg.Provider, "system")
	printProviders(os.Stdout, cfg, def)
	fmt.Printf("\nDefault: %s. Change it with: tts config set provider NAME\n", def)
	return nil
}

func printProviders(w io.Writer, cfg config.Config, def string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, info := range provider.All() {
		mark := " "
		if info.Name == def {
			mark = "*"
		}
		ok, detail := status(info, cfg)
		state := "ready"
		if !ok {
			state = "unavailable"
		}
		where := "cloud"
		if info.Local {
			where = "local"
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\n", mark, info.Name, where, state, detail)
	}
	tw.Flush()
}

func cmdConfig(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	sub := "show"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "show":
		fmt.Println("# " + config.Path())
		kv := cfg.Flatten()
		if len(kv) == 0 {
			fmt.Println("(empty: using defaults; provider = system)")
		}
		for _, p := range kv {
			fmt.Printf("%s = %s\n", p[0], p[1])
		}
		return nil
	case "path":
		fmt.Println(config.Path())
		return nil
	case "get":
		if len(args) != 1 {
			return usagef("usage: tts config get KEY")
		}
		v, err := cfg.Get(args[0])
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "set", "unset":
		if sub == "set" && len(args) < 2 || sub == "unset" && len(args) != 1 {
			return usagef("usage: tts config set KEY VALUE | tts config unset KEY")
		}
		if err := cfg.Set(args[0], strings.Join(args[1:], " ")); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		if sub == "set" {
			fmt.Printf("Set %s = %s\n", args[0], strings.Join(args[1:], " "))
		} else {
			fmt.Printf("Unset %s\n", args[0])
		}
		return nil
	}
	return usagef("unknown config command %q", sub)
}

func cmdAuth(args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if sub == "status" {
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, info := range provider.All() {
			if !info.NeedsKey() {
				continue
			}
			_, src := secrets.Lookup(info.Name, info.KeyEnv)
			if src == "" {
				src = "not set (" + strings.Join(info.KeyEnv, " or ") + ")"
			}
			fmt.Fprintf(tw, "%s\t%s\n", info.Name, src)
		}
		return tw.Flush()
	}
	if len(args) != 1 {
		return usagef("usage: tts auth %s PROVIDER", sub)
	}
	info, err := lookupProvider(args[0])
	if err != nil {
		return err
	}
	if !info.NeedsKey() {
		return fmt.Errorf("%s doesn't use an API key", info.Name)
	}
	switch sub {
	case "set":
		var key string
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintf(os.Stderr, "Paste your %s API key (input hidden): ", info.Name)
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return err
			}
			key = string(b)
		} else {
			key, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		}
		if err := secrets.Store(info.Name, key); err != nil {
			return err
		}
		fmt.Printf("Saved the %s key to the OS keyring (service skill-tts, account %s).\n", info.Name, info.Name)
		return nil
	case "delete":
		if err := secrets.Delete(info.Name); err != nil {
			return err
		}
		fmt.Printf("Deleted the %s key from the OS keyring.\n", info.Name)
		return nil
	}
	return usagef("unknown auth command %q", sub)
}

func cmdDoctor([]string) error {
	cmdVersion(nil)
	cfg, err := config.Load()
	cfgState := "found"
	if err != nil {
		cfgState = "ERROR: " + err.Error()
	} else if _, statErr := os.Stat(config.Path()); statErr != nil {
		cfgState = "not created yet; using defaults"
	}
	def := first(os.Getenv("TTS_PROVIDER"), cfg.Provider, "system")
	out := audio.Open(provider.PCM24k)
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "config\t%s (%s)\n", config.Path(), cfgState)
	fmt.Fprintf(tw, "provider\t%s\n", def)
	fmt.Fprintf(tw, "audio\t%s\n", out.Backend())
	fmt.Fprintf(tw, "state\t%s\n", session.Dir())
	tw.Flush()
	fmt.Println("\nproviders:")
	printProviders(os.Stdout, cfg, def)
	if b, err := os.ReadFile(session.LogPath()); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		fmt.Printf("\nlast background error:\n  %s\n", lines[len(lines)-1])
	}
	return nil
}
