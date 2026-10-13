// Voice mode helpers: local STT/TTS go through the Hub backend (faster-whisper
// + Piper, see internal/voice); the browser only does mic capture, voice-
// activity detection and audio playback.

export type VoiceState = "listening" | "hearing" | "transcribing" | "thinking" | "speaking";

export type ReplyMode = "voice" | "text";

const REPLY_KEY = "hub.voice.reply";

// Spoken replies are the default; the choice is remembered per browser.
export function loadReplyMode(): ReplyMode {
  return localStorage.getItem(REPLY_KEY) === "text" ? "text" : "voice";
}
export function saveReplyMode(m: ReplyMode) {
  localStorage.setItem(REPLY_KEY, m);
}

export async function voiceAvailable(): Promise<boolean> {
  if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) return false;
  try {
    const r = await fetch("/api/voice");
    return r.ok && (await r.json()).available === true;
  } catch {
    return false;
  }
}

export async function transcribe(blob: Blob): Promise<string> {
  const r = await fetch("/api/voice/stt?lang=fr", { method: "POST", body: blob });
  if (!r.ok) throw new Error(await r.text());
  return ((await r.json()).text as string).trim();
}

// toSpeech strips markdown/code so the synthesizer reads prose, not syntax.
export function toSpeech(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, " ")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/https?:\/\/\S+/g, " ")
    .replace(/^\s{0,3}#{1,6}\s*/gm, "")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/[*_~>|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

function sentences(text: string): string[] {
  const parts = text.match(/[^.!?…:;\n]+[.!?…:;]*/g) ?? [text];
  // Merge very short fragments so we don't synthesize one request per word.
  const out: string[] = [];
  for (const p of parts.map((s) => s.trim()).filter(Boolean)) {
    if (out.length && out[out.length - 1].length < 40) out[out.length - 1] += " " + p;
    else out.push(p);
  }
  return out;
}

// Speaker synthesizes sentences ahead of playback and plays them in order.
export class Speaker {
  private queue: Promise<string | null>[] = [];
  private playing = false;
  private audio: HTMLAudioElement | null = null;
  private epoch = 0;

  constructor(private onActive: (active: boolean) => void) {}

  get active() {
    return this.playing || this.queue.length > 0;
  }

  speak(markdown: string) {
    const text = toSpeech(markdown);
    if (!text) return;
    const epoch = this.epoch;
    for (const s of sentences(text)) {
      this.queue.push(
        fetch("/api/voice/tts", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ text: s }),
        })
          .then(async (r) => (r.ok && epoch === this.epoch ? URL.createObjectURL(await r.blob()) : null))
          .catch(() => null),
      );
    }
    void this.pump();
  }

  private async pump() {
    if (this.playing) return;
    this.playing = true;
    this.onActive(true);
    const epoch = this.epoch;
    while (this.queue.length && epoch === this.epoch) {
      const url = await this.queue.shift()!;
      if (!url || epoch !== this.epoch) continue;
      await new Promise<void>((resolve) => {
        const a = new Audio(url);
        this.audio = a;
        const done = () => {
          URL.revokeObjectURL(url);
          resolve();
        };
        a.onended = done;
        a.onerror = done;
        a.play().catch(done);
      });
    }
    if (epoch === this.epoch) {
      this.playing = false;
      this.audio = null;
      this.onActive(false);
    }
  }

  // stop cuts playback immediately and drops anything still queued (barge-in).
  stop() {
    this.epoch++;
    this.queue = [];
    this.audio?.pause();
    this.audio = null;
    if (this.playing) this.onActive(false);
    this.playing = false;
  }
}

export type ListenerHandlers = {
  onSpeechStart: () => void;
  onUtterance: (blob: Blob) => void;
  onError: (msg: string) => void;
};

// Listener keeps the mic open, detects speech with an adaptive energy gate and
// emits one audio blob per utterance (speech followed by ~0.8s of silence).
export class Listener {
  private stream: MediaStream | null = null;
  private ctx: AudioContext | null = null;
  private rec: MediaRecorder | null = null;
  private chunks: Blob[] = [];
  private timer = 0;
  private spoke = false;
  private speechMs = 0;
  private silenceMs = 0;
  private recStarted = 0;
  private noise = 0.01;
  private stopped = false;
  // Raise the gate while the assistant talks so speaker bleed does not count as
  // the user barging in.
  threshold = 3;
  paused = false;

  constructor(private h: ListenerHandlers) {}

  async start() {
    try {
      this.stream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
      });
    } catch {
      this.h.onError("mic");
      return;
    }
    this.ctx = new AudioContext();
    const analyser = this.ctx.createAnalyser();
    analyser.fftSize = 1024;
    this.ctx.createMediaStreamSource(this.stream).connect(analyser);
    const buf = new Float32Array(analyser.fftSize);
    this.startRec();
    const TICK = 50;
    this.timer = window.setInterval(() => {
      analyser.getFloatTimeDomainData(buf);
      let sum = 0;
      for (const v of buf) sum += v * v;
      const rms = Math.sqrt(sum / buf.length);
      const loud = rms > Math.max(0.02, this.noise * this.threshold);
      if (!loud) this.noise = this.noise * 0.95 + rms * 0.05;
      if (this.paused) return;
      if (loud) {
        this.silenceMs = 0;
        this.speechMs += TICK;
        // ~150ms of sustained energy counts as speech (ignores clicks/coughs).
        if (!this.spoke && this.speechMs >= 150) {
          this.spoke = true;
          this.h.onSpeechStart();
        }
      } else if (this.spoke) {
        this.silenceMs += TICK;
        if (this.silenceMs >= 800) this.finish();
      } else {
        this.speechMs = 0;
        // Idle: recycle the recorder so its buffer doesn't grow unbounded.
        if (Date.now() - this.recStarted > 20000) this.restartRec(false);
      }
    }, TICK);
  }

  private startRec() {
    if (!this.stream || this.stopped) return;
    const chunks: Blob[] = [];
    this.chunks = chunks;
    const rec = new MediaRecorder(this.stream);
    rec.ondataavailable = (e) => e.data.size && chunks.push(e.data);
    rec.start();
    this.rec = rec;
    this.recStarted = Date.now();
  }

  private restartRec(emit: boolean) {
    const rec = this.rec;
    if (!rec) return;
    const chunks = this.chunks;
    rec.onstop = () => {
      if (emit && chunks.length) this.h.onUtterance(new Blob(chunks, { type: rec.mimeType }));
    };
    rec.stop();
    this.startRec();
  }

  private finish() {
    this.spoke = false;
    this.speechMs = 0;
    this.silenceMs = 0;
    this.restartRec(true);
  }

  stop() {
    this.stopped = true;
    clearInterval(this.timer);
    this.rec?.state === "recording" && this.rec.stop();
    this.stream?.getTracks().forEach((t) => t.stop());
    this.ctx?.close();
  }
}

// Dictation records one utterance on demand (click to start, click to stop) and
// hands back the audio blob for local STT. Unlike Listener it has no voice-
// activity detection: the user decides when the sentence is over.
export class Dictation {
  private stream: MediaStream | null = null;
  private rec: MediaRecorder | null = null;
  private chunks: Blob[] = [];

  async start() {
    this.stream = await navigator.mediaDevices.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
    this.chunks = [];
    const rec = new MediaRecorder(this.stream);
    rec.ondataavailable = (e) => e.data.size && this.chunks.push(e.data);
    rec.start();
    this.rec = rec;
  }

  stop(): Promise<Blob | null> {
    const rec = this.rec;
    return new Promise((resolve) => {
      const release = () => {
        this.stream?.getTracks().forEach((t) => t.stop());
        this.stream = null;
        this.rec = null;
      };
      if (!rec || rec.state === "inactive") {
        release();
        return resolve(null);
      }
      rec.onstop = () => {
        const blob = this.chunks.length ? new Blob(this.chunks, { type: rec.mimeType }) : null;
        release();
        resolve(blob);
      };
      rec.stop();
    });
  }
}
