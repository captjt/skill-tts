# Provider reference

> **Testing status:** `system` and `kokoro` have been tested live. `openai`, `elevenlabs`, `gemini`, `cartesia`, and `deepgram` were built from their API docs and unit-tested against fake servers, but have not yet been run against the real APIs. Expect possible request-format bugs, especially for Gemini.

All providers stream 16-bit PCM straight to the speaker, so playback starts on the first bytes. `tts render` uses the high-fidelity model where a provider has one, and the live-speech model otherwise. Set per-provider defaults with `tts config set <provider>.<voice|model|speed|instructions|base_url> VALUE`.

| Provider | Runs | Key (env var, or `tts auth set NAME`) | Default voice | Default model | Style (`-i`) | Speed (`-s`) |
|---|---|---|---|---|---|---|
| `system` | local, offline | none | best installed English voice (macOS); espeak default (Linux) | – | no | yes |
| `kokoro` | local server | none | `af_heart` | `kokoro` | no | yes |
| `openai` | cloud | `OPENAI_API_KEY` | `marin` | `gpt-4o-mini-tts` | yes (gpt-4o-mini-tts) | yes |
| `elevenlabs` | cloud | `ELEVENLABS_API_KEY` (or `ELEVEN_API_KEY`) | `JBFqnCBsd6RMkjVDRZzb` (George) | `eleven_flash_v2_5` | no | 0.7–1.2 |
| `gemini` | cloud | `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) | `Kore` | `gemini-3.8-flash-lite-tts` | yes | no; ask for pace in `-i` |
| `cartesia` | cloud | `CARTESIA_API_KEY` | `db6b0ed5-d5d3-463d-ae85-518a07d3c2b4` | `sonic-3.6` | no | yes |
| `deepgram` | cloud | `DEEPGRAM_API_KEY` | `aura-2-thalia-en` | (voice is the model) | no | yes |

## system

- **macOS** uses `say`. Download higher-quality voices in System Settings → Accessibility → Spoken Content → System Voice → Manage Voices. "Premium" and "Enhanced" English voices are picked automatically when installed. Siri voices can't be used from the command line. List voices with `tts voices -p system en_`.
- **Linux** uses `espeak-ng`: `sudo apt install espeak-ng`. Voices are language codes such as `en-us` or `en-gb`.

## kokoro

Kokoro-82M is an open-weight model that sounds close to the cloud providers. It runs through any server that speaks the OpenAI speech API, such as [Kokoro-FastAPI](https://github.com/remsky/Kokoro-FastAPI):

```bash
docker run -d --name kokoro -p 8880:8880 ghcr.io/remsky/kokoro-fastapi-cpu:v0.9.0
```

- **Other servers:** point at another host or port with `KOKORO_BASE_URL` or `tts config set kokoro.base_url http://host:port/v1`.
- **Voices** are named by language and gender prefix: `af_` American female, `am_` American male, `bf_`/`bm_` British, then `e`, `f`, `h`, `i`, `j`, `p`, `z` for Spanish, French, Hindi, Italian, Japanese, Portuguese, and Mandarin. Good English voices: `af_heart`, `af_bella`, `af_nicole`, `am_michael`, `am_fenrir`, `bf_emma`, `bm_george`. Kokoro-FastAPI also accepts blends like `af_bella+af_sky`.
- **Fast start:** tts sends the opening clause as its own request, so speech starts in about half a second even on CPU. The rest is generated while that clause plays.

## openai

- **Voices:** `marin` and `cedar` sound best. The others are `alloy`, `ash`, `ballad`, `coral`, `echo`, `fable`, `nova`, `onyx`, `sage`, `shimmer`, and `verse`.
- **Models:** `gpt-4o-mini-tts` (steerable with `-i`), `tts-1` (lower latency), and `tts-1-hd`. `tts-1*` supports fewer voices and ignores `-i`.
- **Base URL:** `TTS_OPENAI_BASE_URL` points it at a compatible proxy.

## elevenlabs

- **Voices:** pass a voice ID, or a name from your voice library (`-v Rachel`). IDs skip a lookup request. `tts voices -p elevenlabs` lists your library.
- **Models:** `eleven_flash_v2_5` (lowest latency, ~75 ms; the default for live speech), `eleven_multilingual_v2` (the default for `tts render`), `eleven_v4_turbo` (expressive, real-time), `eleven_v3`, and `eleven_v4`.

## gemini

- **Voices:** Zephyr, Puck, Charon, Kore, Fenrir, Leda, Orus, Aoede, Callirrhoe, Autonoe, Enceladus, Iapetus, Umbriel, Algieba, Despina, Erinome, Algenib, Rasalgethi, Laomedeia, Achernar, Alnilam, Schedar, Gacrux, Pulcherrima, Achird, Zubenelgenubi, Vindemiatrix, Sadachbia, Sadaltager, and Sulafat.
- **Models:** `gemini-3.8-flash-lite-tts` (fast; the default for live speech) and `gemini-3.8-flash-tts` (highest fidelity; the default for `tts render`).
- **Style:** `-i` sets the delivery style ("cheerful and friendly", "whispering", "speaking slowly").
- **Inline tags:** angle-bracket tags in the text, such as `<laugh>`, `<sigh>`, and `<short pause>`, are passed through.

## cartesia

- **Voices:** `tts voices -p cartesia` lists them. Pass a voice ID with `-v`.
- **Models:** `sonic-3.6` (the default), `sonic-3.5`, `sonic-3`, and `sonic-latest`.

## deepgram

- **Voices:** a voice is a model. `-v thalia` is shorthand for `aura-2-thalia-en`. Others include andromeda, apollo, asteria, athena, helena, hermes, luna, orion, orpheus, and zeus (`tts voices -p deepgram`).
- **Request size:** up to 2,000 characters per request. Longer text is split automatically.
