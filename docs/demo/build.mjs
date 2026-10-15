// Builds docs/demo/demo.mp4: captures demo.html frame by frame with headless
// Chrome (DevTools protocol over Node's built-in WebSocket; no npm packages),
// then muxes the real narration from session/voiceover.wav with ffmpeg.
//
//   node docs/demo/build.mjs            # build demo.mp4 + poster.png
//   node docs/demo/build.mjs --frames 0,90,300   # just write those frames as PNGs (for checking)
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const CHROME = process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const FPS = 30, SCALE = 1.5, W = 1280, H = 720;
const PART_B = 9.0, TAIL = 1.3; // narration starts at PART_B; hold the last scene for TAIL

// ---------- data.js from the real session artifacts ----------
const S = join(here, "session");
const session = JSON.parse(readFileSync(join(S, "session.json"), "utf8"));
const manifest = JSON.parse(readFileSync(join(S, "manifest.json"), "utf8"));
const cues = parseSRT(readFileSync(join(S, "voiceover.srt"), "utf8"));
const env = envelope(readFileSync(join(S, "voiceover.wav")), FPS);
const total = PART_B + manifest.duration + TAIL;
const scenes = [
  `<div class="h1" data-in="0">Written by Claude.</div>
   <div class="h1 grad" data-in="0.55">Voiced by ElevenLabs.</div>`,
  `<div class="flow">
     <div class="card" data-in="0.1"><div class="k">script.json</div><div class="v">3 scenes</div><div class="s">written by Claude</div></div>
     <div class="arrow" data-in="0.9">→</div>
     <div class="card" data-in="1.0"><div class="k">tts render</div><div class="v">ElevenLabs</div><div class="s">voice “Eric”, scenes in parallel</div></div>
     <div class="arrow" data-in="2.0">→</div>
     <div class="card" data-in="2.1"><div class="k">voiceover/</div><div class="v">WAV per scene</div><div class="s">+ captions + manifest.json<br>with exact timings</div></div>
   </div>`,
  `<div class="install" data-in="0"><span class="p">$</span>npx skills add captjt/skill-tts</div>
   <div class="url" data-in="0.6">github.com/captjt/skill-tts</div>`,
];
const data = {
  fps: FPS, partB: PART_B, total, session, manifest, cues, env, scenes,
  voiceLabel: "ElevenLabs · Eric · eleven_multilingual_v2",
  audioSrc: "session/voiceover.wav",
};
writeFileSync(join(here, "data.js"), `window.DEMO = ${JSON.stringify(data)};\n`);
console.log(`data.js: ${total.toFixed(2)}s, ${Math.round(total * FPS)} frames, narration ${manifest.duration}s`);

// ---------- capture ----------
const only = process.argv.includes("--frames")
  ? process.argv[process.argv.indexOf("--frames") + 1].split(",").map(Number)
  : null;
const frameDir = join(here, ".frames");
rmSync(frameDir, { recursive: true, force: true });
mkdirSync(frameDir, { recursive: true });

const port = 9300 + Math.floor(Math.random() * 500);
const chrome = spawn(CHROME, [
  "--headless=new", "--disable-gpu", "--hide-scrollbars", "--mute-audio",
  `--remote-debugging-port=${port}`, "--allow-file-access-from-files",
  `--user-data-dir=${join(here, ".chrome-profile")}`, "about:blank",
], { stdio: "ignore" });
try {
  const page = await connect(port, `file://${join(here, "demo.html")}`);
  await page.send("Emulation.setDeviceMetricsOverride", { width: W, height: H, deviceScaleFactor: SCALE, mobile: false });
  await waitFor(async () => (await page.eval("typeof window.render === 'function' && document.readyState === 'complete'")) === true);
  const frames = only ?? [...Array(Math.round(total * FPS)).keys()];
  for (const [n, i] of frames.entries()) {
    await page.eval(`render(${i / FPS})`);
    const { data: png } = await page.send("Page.captureScreenshot", { format: "png" });
    writeFileSync(join(frameDir, only ? `frame-${i}.png` : `${String(i).padStart(5, "0")}.png`), Buffer.from(png, "base64"));
    if (n % 60 === 0) process.stdout.write(`\rframes ${n}/${frames.length}`);
  }
  console.log(`\rframes ${frames.length}/${frames.length}`);
  page.close();
} finally {
  const exited = new Promise((r) => chrome.once("exit", r));
  chrome.kill();
  await exited;
  rmSync(join(here, ".chrome-profile"), { recursive: true, force: true, maxRetries: 5 });
}
if (only) process.exit(0);

// ---------- encode ----------
const out = join(here, "demo.mp4");
execFileSync("ffmpeg", [
  "-y", "-hide_banner", "-loglevel", "error",
  "-framerate", String(FPS), "-i", join(frameDir, "%05d.png"),
  "-i", join(S, "voiceover.wav"),
  "-filter_complex", `[1:a]adelay=${Math.round(PART_B * 1000)}:all=1,apad,aresample=48000[a]`,
  "-map", "0:v", "-map", "[a]", "-t", total.toFixed(3),
  "-c:v", "libx264", "-preset", "slow", "-crf", "20", "-pix_fmt", "yuv420p", "-tune", "stillimage",
  "-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", out,
], { stdio: "inherit" });
const poster = Math.round((PART_B + manifest.segments[1].start + 2.6) * FPS);
execFileSync("cp", [join(frameDir, `${String(poster).padStart(5, "0")}.png`), join(here, "poster.png")]);
rmSync(frameDir, { recursive: true, force: true });
console.log(`wrote ${out}`);

// ---------- helpers ----------
async function connect(port, url) {
  const base = `http://127.0.0.1:${port}`;
  await waitFor(async () => (await fetch(`${base}/json/version`).catch(() => null))?.ok);
  const target = await (await fetch(`${base}/json/new?${url}`, { method: "PUT" })).json();
  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((ok, fail) => { ws.onopen = ok; ws.onerror = fail; });
  let id = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      const { ok, fail } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? fail(new Error(msg.error.message)) : ok(msg.result);
    }
  };
  const send = (method, params = {}) => new Promise((ok, fail) => {
    pending.set(++id, { ok, fail });
    ws.send(JSON.stringify({ id, method, params }));
  });
  const evalJS = async (expression) => {
    const r = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || "eval failed");
    return r.result.value;
  };
  return { send, eval: evalJS, close: () => ws.close() };
}

async function waitFor(check, ms = 15000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) {
    try { if (await check()) return; } catch {}
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("timed out");
}

function parseSRT(text) {
  const ts = (s) => { const [h, m, rest] = s.split(":"); const [sec, ms] = rest.split(","); return +h * 3600 + +m * 60 + +sec + +ms / 1000; };
  return text.trim().split(/\n\s*\n/).map((block) => {
    const lines = block.split("\n");
    const [a, b] = lines[1].split(" --> ");
    return { start: ts(a.trim()), end: ts(b.trim()), text: lines.slice(2).join(" ") };
  });
}

// Per-frame loudness (0..1) of a 16-bit mono WAV, for the audio-reactive waveform.
function envelope(buf, fps) {
  let off = 12, rate = 24000, data = null;
  while (off < buf.length) {
    const id = buf.toString("ascii", off, off + 4), size = buf.readUInt32LE(off + 4);
    if (id === "fmt ") rate = buf.readUInt32LE(off + 12);
    if (id === "data") { data = buf.subarray(off + 8, off + 8 + size); break; }
    off += 8 + size + (size % 2);
  }
  const per = Math.round(rate / fps), out = [];
  for (let i = 0; i + 1 < data.length; i += per * 2) {
    let sum = 0, n = 0;
    for (let j = i; j < Math.min(i + per * 2, data.length - 1); j += 2) { const v = data.readInt16LE(j) / 32768; sum += v * v; n++; }
    out.push(Math.sqrt(sum / Math.max(n, 1)));
  }
  const peak = [...out].sort((a, b) => a - b)[Math.floor(out.length * 0.97)] || 1;
  return out.map((v) => Math.round(Math.min(1, Math.sqrt(v / peak)) * 1000) / 1000);
}
