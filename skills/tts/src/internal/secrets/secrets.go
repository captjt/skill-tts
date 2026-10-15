// Package secrets finds provider API keys. Lookup order:
//  1. environment variables (e.g. OPENAI_API_KEY)
//  2. the macOS Keychain (service "skill-tts", account = provider name), or
//     the Secret Service keyring on Linux via secret-tool.
//
// Keys are never written to disk by this program and never passed on a
// command line, where other processes could read them.
package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const service = "skill-tts"

// Lookup returns the key for provider and where it came from.
func Lookup(provider string, envVars []string) (key, source string) {
	for _, v := range envVars {
		if k := strings.TrimSpace(os.Getenv(v)); k != "" {
			return k, "env " + v
		}
	}
	if k := keyringGet(provider); k != "" {
		return k, keyringName()
	}
	return "", ""
}

func keyringName() string {
	if runtime.GOOS == "darwin" {
		return "macOS Keychain"
	}
	return "Secret Service keyring"
}

func keyringGet(provider string) string {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", provider, "-w")
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return ""
		}
		cmd = exec.Command("secret-tool", "lookup", "service", service, "account", provider)
	default:
		return ""
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Store saves key in the OS keyring.
func Store(provider, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("empty key")
	}
	for _, r := range key {
		if r <= ' ' || r > '~' || r == '"' || r == '\\' {
			return errors.New("key contains unexpected characters")
		}
	}
	switch runtime.GOOS {
	case "darwin":
		// `security -i` reads the command from stdin, keeping the key out of argv.
		cmd := exec.Command("/usr/bin/security", "-i")
		cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -l \"skill-tts %s\" -w \"%s\"\n", service, provider, provider, key))
		return run(cmd)
	case "linux":
		if _, err := exec.LookPath("secret-tool"); err != nil {
			return errors.New("secret-tool not found (install libsecret-tools), or set the env var instead")
		}
		cmd := exec.Command("secret-tool", "store", "--label=skill-tts "+provider, "service", service, "account", provider)
		cmd.Stdin = strings.NewReader(key)
		return run(cmd)
	}
	return fmt.Errorf("no keyring support on %s; set the env var instead", runtime.GOOS)
}

// Delete removes the key from the OS keyring.
func Delete(provider string) error {
	switch runtime.GOOS {
	case "darwin":
		return run(exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", provider))
	case "linux":
		return run(exec.Command("secret-tool", "clear", "service", service, "account", provider))
	}
	return fmt.Errorf("no keyring support on %s", runtime.GOOS)
}

func run(cmd *exec.Cmd) error {
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(out.String())
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}
