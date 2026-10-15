# skill-tts

Text to speech for Claude Code and other coding agents. Voices come from the built-in system voice, local [Kokoro](https://github.com/remsky/Kokoro-FastAPI), OpenAI, ElevenLabs, Gemini, Cartesia, or Deepgram. It's built to be one block in a larger pipeline:

- **Voiceovers for videos:** `tts render` turns a scene-by-scene script into trimmed audio files, a combined track, and captions. It also writes a JSON manifest of exact durations and offsets that the rest of the pipeline (Remotion, ffmpeg, an editor) can time against. Re-runs only re-render the scenes you changed.
- **Spoken output:** read a summary aloud, or announce when a long task finishes.

What you get:
- **Fast:** audio streams straight from the provider to CoreAudio (or PulseAudio/ALSA) in-process, so speech starts on the first bytes. The command returns as soon as audio is playing.
- **Trustworthy:** a small Go binary that you can rebuild byte-for-byte from the source in this repo. It is checksum-verified before it ever runs, and it sends no telemetry. The default provider never leaves your machine.
- **Native on macOS**, works on Linux. Apple Silicon and Intel, arm64 and x86-64.

## Install

```bash
npx skills add captjt/skill-tts
```

Or as a Claude Code plugin:

```
/plugin marketplace add captjt/skill-tts
/plugin install tts@skill-tts
```

Then ask Claude something like:
- *"write and render a voiceover for the scenes in storyboard.md"*
- *"read me that summary out loud"*

It works immediately with your system voice. To use a better voice, see [Providers](#providers).

The skill runs `skills/tts/scripts/tts`, a short POSIX shell launcher. On first use, the launcher downloads the `tts` binary for your platform from this repo's GitHub Releases. It refuses to run the binary unless its SHA-256 matches the hash pinned in the launcher. See [Trust model](#trust-model).

## Voiceovers for videos

Write one segment per scene:

```json
{
  "provider": "elevenlabs",
  "voice": "JBFqnCBsd6RMkjVDRZzb",
  "instructions": "warm, confident product narrator",
  "pause": 0.5,
  "segments": [
    {"id": "01-intro",   "text": "Meet the new Insights dashboard."},
    {"id": "02-filters", "text": "Filter any chart in one click: by team, by service, or by release."},
    {"id": "03-outro",   "text": "Insights is available today, on every plan.", "pause": 1.0}
  ]
}
```

```bash
tts render script.json -o voiceover/
```

That produces:

| File | Contents |
|---|---|
| `voiceover/<id>.wav` | each segment, 24 kHz mono, leading and trailing silence trimmed so `duration` is the spoken length |
| `voiceover/voiceover.wav` | every segment joined with its pause (`--format mp3` or `m4a` for other formats, `--no-combine` to skip) |
| `voiceover/voiceover.srt` | sentence-level captions. Segment boundaries are exact; sentence timing inside a segment is estimated from character counts |
| `voiceover/manifest.json` | provider, sample rate, and for each segment: file, `duration`, `start`/`end` offset in the combined track, text, voice, content hash, and whether it was cached. Also printed to stdout |

**Caching.** Each segment is keyed by its text, voice, model, style, and speed. Re-running reuses unchanged segments, so editing one line re-renders and re-bills only that line. `--force` re-renders everything.

**Reliability.** Segments render in parallel: three at a time for cloud providers, one for local ones. Rate limits and server errors are retried with backoff. If some segments fail, the rest are kept and the next run retries only the failures.

**Quality defaults.** `render` uses each provider's highest-fidelity model unless you choose one: `eleven_multilingual_v2` for ElevenLabs and `gemini-3.8-flash-tts` for Gemini. Live speech uses the low-latency models. Set a separate default provider for voiceovers with `tts config set voiceover_provider elevenlabs`.

To lay the track under a video:

```bash
ffmpeg -i demo.mp4 -i voiceover/voiceover.wav -map 0:v -map 1:a -c:v copy -c:a aac -shortest demo-narrated.mp4
```

## Using the CLI directly

```bash
tts "Deploy finished. Everything is green."           # returns once audio starts
tts -p openai -v cedar -i "warm, unhurried" < notes.md   # Markdown is cleaned up for speech
tts -p kokoro -o intro.wav --json "Welcome to the demo." # write a file instead; --json prints path + duration
tts --queue "Step two done."                             # play after current speech instead of interrupting
tts stop                                                 # silence everything
tts doctor                                               # audio output, providers, keys
```

`tts` is `skills/tts/scripts/tts` from wherever the skill was installed. For example, a global `npx skills add` install puts it at `~/.claude/skills/tts/scripts/tts`.

## Providers

| Provider | Where it runs | Setup |
|---|---|---|
| `system` (default) | your machine, offline | none on macOS (`say`). On Linux: `apt install espeak-ng` |
| `kokoro` | your machine | `docker run -d --name kokoro -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:v0.9.0` |
| `openai` | cloud | `OPENAI_API_KEY` |
| `elevenlabs` | cloud | `ELEVENLABS_API_KEY` |
| `gemini` | cloud | `GEMINI_API_KEY` |
| `cartesia` | cloud | `CARTESIA_API_KEY` |
| `deepgram` | cloud | `DEEPGRAM_API_KEY` |

Pick a default and its voice:

```bash
tts config set provider kokoro
tts config set kokoro.voice af_heart
tts voices -p kokoro          # list voices (any provider)
tts config show
```

Settings live in `~/.config/skill-tts/config.json`. `TTS_PROVIDER` overrides the default provider for one shell. Voices, models, and options for each provider are listed in [skills/tts/references/providers.md](skills/tts/references/providers.md).

### API keys

Keys come from environment variables. If none is set, tts looks in the OS keyring, which keeps keys out of plaintext dotfiles:

```bash
tts auth set openai      # prompts with hidden input; stores in the macOS Keychain / Secret Service
tts auth status          # where each key comes from
tts auth delete openai
```

Run `tts auth set` in your own terminal, not in the agent chat.

## Performance

The latency you notice is time to first audio, so everything is built around streaming:

- **Raw PCM end to end.** Every provider is asked for 24 kHz 16-bit PCM and the bytes go straight to the audio device, so there's no MP3 decoding and no temp files. The audio device opens while the request is in flight.
- **Background playback.** `tts` hands playback to a detached worker and returns when the first audio arrives. Measured on an M-series Mac: 16 ms for the system voice, and about 0.5 s for Kokoro running on CPU in Docker. Cloud providers mostly come down to their own time to first byte. Errors such as a bad key or an unknown voice are still reported before the command returns.
- **Gapless long text.** Text is split at sentence boundaries to fit each provider's request limit. Chunk *n+1* is requested while chunk *n* plays, and never more than one chunk ahead, so you don't pay for audio you stop.
- **Fast start for local models.** Kokoro renders a whole request before it streams anything. For Kokoro, tts sends the opening clause on its own, which cut time to first audio from 1.4 s to 0.6 s in testing.
- **A single 7 MB static binary.** It starts in milliseconds, with no interpreter or runtime to load.

## Trust model

What runs on your machine, and how you can check it:

- **The launcher** (`skills/tts/scripts/tts`) is about 100 lines of POSIX sh. It works out your OS and CPU, downloads `tts-<os>-<arch>` from `github.com/captjt/skill-tts/releases/download/v<VERSION>/` over HTTPS, checks the SHA-256 against the hash pinned in the script, and caches the binary under `~/.cache/skill-tts/bin`. If the hash doesn't match, it refuses to run.
- **Reproducible builds.** Release binaries are built with a pinned Go toolchain, no cgo, `-trimpath`, and no build IDs or VCS stamps. Anyone can rebuild them byte-for-byte, so you don't have to trust the release pipeline:
  ```bash
  git clone https://github.com/captjt/skill-tts && cd skill-tts
  git checkout v0.1.0   # the tag matching VERSION in the launcher
  make verify-pins
  ```
  The release workflow runs the same check and refuses to publish binaries that don't match the pinned hashes. Each binary also carries a [build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations): `gh attestation verify tts-darwin-arm64 --repo captjt/skill-tts`.
- **Or skip the download entirely.** The full source ships inside the skill (`skills/tts/src`). Set `TTS_BUILD_FROM_SOURCE=1` and the launcher builds with your own Go toolchain instead.
- **Small dependency set:** the Go standard library plus [oto](https://github.com/ebitengine/oto) (audio output), [purego](https://github.com/ebitengine/purego), [pulse](https://github.com/jfreymuth/pulse), `golang.org/x/sys`, and `golang.org/x/term`. They are pinned in `go.sum` and checked against the Go checksum database.
- **Network.** tts only contacts the provider you choose. The default, `system`, makes no network requests at all. There's no telemetry and no update checks. HTTP redirects are never followed, so an API key can't be forwarded to another host.
- **Your text.** The default provider and Kokoro keep text on your machine. Cloud providers receive the text you ask them to speak. Text is passed to subprocesses and workers over stdin, never on a command line where other processes could read it, and it's never written to disk unless you ask for an audio file.
- **Your keys.** Keys are read from env vars or the OS keyring. They are never written to the config file (tts refuses to store them there), never logged, and never placed in argv.
- **Cost guardrail.** Paid providers refuse text over 20,000 characters unless you pass `--max-chars` or set `max_chars`.

Report vulnerabilities as described in [SECURITY.md](SECURITY.md).

## Optional: spoken notifications

Claude Code [hooks](https://code.claude.com/docs/en/hooks) can speak when Claude needs your attention. Add this to `~/.claude/settings.json`, adjusting the path to wherever the skill is installed:

```json
{
  "hooks": {
    "Notification": [
      { "hooks": [{ "type": "command", "command": "jq -r .message | ~/.claude/skills/tts/scripts/tts --queue" }] }
    ]
  }
}
```

## Platform support

| | macOS 12+ | Linux |
|---|---|---|
| Audio output | CoreAudio, in-process | PulseAudio/PipeWire or ALSA, in-process. Falls back to ffplay, mpv, paplay, aplay, or sox |
| System voice | `say` | `espeak-ng` |
| Keyring | Keychain | Secret Service (`secret-tool`) |
| Architectures | arm64, amd64 | arm64, amd64 |

Without an audio device (SSH sessions, containers), use `--out file.wav`.

## Development

```bash
make test          # go vet + tests (race detector on)
make build         # dist/tts for this machine
make lint          # shellcheck + gofmt
make verify-pins   # rebuild all release binaries and compare with the launcher's pins
```

To try your working copy through the launcher, run `TTS_BUILD_FROM_SOURCE=1 skills/tts/scripts/tts doctor`.

**Releasing:**

1. Bump `VERSION` in `skills/tts/scripts/tts` and `.claude-plugin/plugin.json`.
2. Run `make pin` and commit.
3. Tag `vX.Y.Z` and push the tag.

CI rebuilds the binaries, verifies them against the pins, attests them, and publishes the release.

## License

[MIT](LICENSE)
