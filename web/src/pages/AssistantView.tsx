import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  api,
  ASSISTANT_EXTS,
  ASSISTANT_MAX_FILE,
  ASSISTANT_MAX_FILES,
  assistantAttachmentUrl,
  type AssistantAttachment,
  type AssistantNightPatch,
  type AssistantState,
  type AssistantMessage,
  type AssistantTrigger,
  type HaEntity,
  type ScheduledTask,
} from "../lib/api";
import { Dictation, transcribe } from "../lib/voice";
import { VoiceBar, useVoiceMode } from "../components/VoiceBar";

const inputCls =
  "w-full bg-bg border border-border rounded px-2 py-1.5 text-sm focus:outline-none focus:border-accent";
const btnCls =
  "text-sm bg-accent text-bg font-medium rounded px-3 py-1.5 hover:bg-accent-dim transition-colors disabled:opacity-40";

const fmtSize = (n: number) =>
  n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : n >= 1 << 10 ? `${Math.round(n / (1 << 10))} KB` : `${n} B`;

// A file picked but not sent yet; preview is an object URL for images.
interface Picked {
  key: string;
  file: File;
  preview?: string;
}

export function AssistantView() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<"chat" | "auto">("chat");
  const [projectId, setProjectId] = useState<string | null>(null);
  const [ready, setReady] = useState<boolean | null>(null);

  useEffect(() => {
    let alive = true;
    const probe = () =>
      api
        .assistantState()
        .then((s) => {
          if (!alive) return;
          setProjectId(s.projectId);
          setReady(true);
        })
        .catch(() => alive && setReady(false));
    probe();
    const timer = setInterval(() => ready === false && probe(), 3000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, [ready]);

  return (
    <div className="h-full flex flex-col max-w-3xl mx-auto w-full">
      <div className="flex items-center gap-1 px-4 pt-4 border-b border-border">
        <h1 className="text-lg font-semibold mr-4">{t("assistant.title")}</h1>
        {(["chat", "auto"] as const).map((k) => (
          <button
            key={k}
            onClick={() => setTab(k)}
            className={`px-3 py-2 text-sm border-b-2 -mb-px transition-colors ${
              tab === k ? "border-accent text-accent" : "border-transparent text-muted hover:text-fg"
            }`}
          >
            {t(k === "chat" ? "assistant.tabChat" : "assistant.tabAuto")}
          </button>
        ))}
      </div>
      {ready === false ? (
        <div className="p-6 text-sm text-muted animate-pulse">{t("assistant.notReady")}</div>
      ) : tab === "chat" ? (
        <Chat />
      ) : (
        projectId && <Automations projectId={projectId} />
      )}
    </div>
  );
}

function Chat() {
  const { t } = useTranslation();
  const [messages, setMessages] = useState<AssistantMessage[]>([]);
  const heldRef = useRef(false); // some message of the thread is held back (night mode)
  heldRef.current = messages.some((m) => m.held);
  const [busy, setBusy] = useState(false);
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [picked, setPicked] = useState<Picked[]>([]);
  const [dragging, setDragging] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);
  const pickedRef = useRef<Picked[]>([]);
  pickedRef.current = picked;
  const [dictating, setDictating] = useState<"idle" | "recording" | "transcribing">("idle");
  const lastId = useRef(0);
  const bottom = useRef<HTMLDivElement>(null);
  const dictation = useRef<Dictation | null>(null);
  const turnPending = useRef(false); // a voice turn is waiting for its answer

  // Live voice: spoken turns go through the same web thread as typed ones.
  const voice = useVoiceMode((body, spoken) => {
    turnPending.current = true;
    setBusy(true);
    api.assistantSend(body, spoken).then(
      () => setTimeout(() => pollRef.current?.(), 400),
      (e) => {
        turnPending.current = false;
        setError(e.message);
        voiceRef.current?.onTurnEnd();
      },
    );
  });
  const voiceRef = useRef(voice);
  voiceRef.current = voice;
  const pollRef = useRef<(() => void) | null>(null);

  const poll = useCallback(async () => {
    try {
      const r = await api.assistantMessages(lastId.current);
      setBusy(r.busy);
      if (r.messages.length > 0) {
        lastId.current = r.messages[r.messages.length - 1].id;
        setMessages((prev) => [...prev, ...r.messages]);
        // Only fresh web answers are spoken (never replayed history, never
        // WhatsApp/automation traffic).
        for (const m of r.messages) {
          if (m.role === "assistant" && m.channel === "web") voiceRef.current?.onAssistantText(m.text);
        }
        if (turnPending.current && !r.busy && r.messages.some((m) => m.role === "assistant")) {
          turnPending.current = false;
          voiceRef.current?.onTurnEnd();
        }
      }
      // Held messages (night mode) change in place when delivered: refresh them.
      if (heldRef.current) {
        const all = await api.assistantMessages(0);
        const byId = new Map(all.messages.map((m) => [m.id, m]));
        setMessages((prev) => prev.map((m) => (m.held && byId.has(m.id) ? byId.get(m.id)! : m)));
      }
    } catch {
      /* transient: the next tick retries */
    }
  }, []);
  pollRef.current = poll;

  useEffect(() => {
    poll();
    const timer = setInterval(poll, voice.on ? 700 : 2000);
    return () => clearInterval(timer);
  }, [poll, voice.on]);

  useEffect(() => () => void dictation.current?.stop(), []);
  useEffect(
    () => () => pickedRef.current.forEach((p) => p.preview && URL.revokeObjectURL(p.preview)),
    [],
  );

  // Validates (like the server will) and queues files for the next message.
  function addFiles(files: File[]) {
    setError(null);
    const room = ASSISTANT_MAX_FILES - pickedRef.current.length;
    const ok: Picked[] = [];
    for (const f of files) {
      const ext = f.name.includes(".") ? f.name.split(".").pop()!.toLowerCase() : "";
      const known = ASSISTANT_EXTS.includes(ext) || (!ext && /^image\/(jpeg|png|gif|webp)$/.test(f.type));
      if (!known) return setError(t("assistant.attachBadType", { name: f.name || "?" }));
      if (f.size > ASSISTANT_MAX_FILE)
        return setError(t("assistant.attachTooBig", { name: f.name, max: ASSISTANT_MAX_FILE >> 20 }));
      if (ok.length >= room) return setError(t("assistant.attachTooMany", { max: ASSISTANT_MAX_FILES }));
      ok.push({
        key: `${Date.now()}-${Math.random()}`,
        file: f,
        preview: f.type.startsWith("image/") ? URL.createObjectURL(f) : undefined,
      });
    }
    setPicked((prev) => [...prev, ...ok]);
  }
  function unpick(key: string) {
    setPicked((prev) => {
      prev.filter((p) => p.key === key).forEach((p) => p.preview && URL.revokeObjectURL(p.preview));
      return prev.filter((p) => p.key !== key);
    });
  }

  async function toggleDictation() {
    setError(null);
    if (dictating === "recording") {
      setDictating("transcribing");
      try {
        const blob = await dictation.current?.stop();
        const said = blob ? await transcribe(blob) : "";
        if (said) setText((prev) => (prev && !/\s$/.test(prev) ? prev + " " : prev) + said);
      } catch (e: any) {
        setError(String(e?.message ?? e));
      } finally {
        setDictating("idle");
      }
      return;
    }
    if (dictating !== "idle") return;
    try {
      dictation.current = new Dictation();
      await dictation.current.start();
      setDictating("recording");
    } catch {
      setError(t("voice.micDenied"));
    }
  }

  useEffect(() => {
    bottom.current?.scrollIntoView({ behavior: "smooth", block: "end" });
  }, [messages.length, busy]);

  async function send() {
    const body = text.trim();
    if ((!body && picked.length === 0) || sending) return;
    setSending(true);
    setError(null);
    try {
      let ids: string[] = [];
      if (picked.length > 0) {
        ids = (await api.assistantUpload(picked.map((p) => p.file))).attachments.map((a) => a.id);
      }
      await api.assistantSend(body, false, ids);
      picked.forEach((p) => p.preview && URL.revokeObjectURL(p.preview));
      setPicked([]);
      setText("");
      setBusy(true);
      setTimeout(poll, 400);
    } catch (e: any) {
      setError(e.message);
    } finally {
      setSending(false);
    }
  }

  async function reset(history: boolean) {
    if (history && !confirm(t("assistant.confirmClear"))) return;
    await api.assistantReset(history).catch((e) => setError(e.message));
    if (history) {
      setMessages([]);
      lastId.current = 0;
    }
  }

  return (
    <div
      className="flex-1 min-h-0 flex flex-col relative"
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes("Files")) {
          e.preventDefault();
          setDragging(true);
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setDragging(false);
      }}
      onDrop={(e) => {
        e.preventDefault();
        setDragging(false);
        addFiles(Array.from(e.dataTransfer.files));
      }}
    >
      {dragging && (
        <div className="absolute inset-0 z-10 flex items-center justify-center bg-bg/80 border-2 border-dashed border-accent rounded text-sm text-accent pointer-events-none">
          {t("assistant.attachDrop")}
        </div>
      )}
      <div className="flex-1 min-h-0 overflow-auto px-4 py-4 space-y-3">
        {messages.length === 0 && !busy && (
          <p className="text-sm text-muted text-center mt-10">{t("assistant.empty")}</p>
        )}
        {messages.map((m) => (
          <Bubble key={m.id} m={m} />
        ))}
        {busy && (
          <div className="text-xs text-muted animate-pulse flex items-center gap-2">
            <span className="inline-block h-2 w-2 rounded-full bg-accent animate-pulse" />
            {t("assistant.thinking")}
          </div>
        )}
        <div ref={bottom} />
      </div>
      {error && <div className="text-danger text-xs px-4 pb-1">{error}</div>}
      <div className="border-t border-border p-3 space-y-2">
        <VoiceBar v={voice} />
        {picked.length > 0 && (
          <ul className="flex flex-wrap gap-2">
            {picked.map((p) => (
              <li
                key={p.key}
                className="relative flex items-center gap-2 border border-border rounded bg-panel-2 pr-6 text-xs max-w-[14rem]"
              >
                {p.preview ? (
                  <img src={p.preview} alt="" className="h-12 w-12 object-cover rounded-l" />
                ) : (
                  <span className="px-2 py-3 text-lg">📄</span>
                )}
                <span className="min-w-0 py-1">
                  <span className="block truncate" title={p.file.name}>
                    {p.file.name}
                  </span>
                  <span className="text-muted">{fmtSize(p.file.size)}</span>
                </span>
                <button
                  type="button"
                  onClick={() => unpick(p.key)}
                  aria-label={t("assistant.attachRemove", { name: p.file.name })}
                  title={t("assistant.attachRemove", { name: p.file.name })}
                  className="absolute top-0.5 right-1 text-muted hover:text-danger"
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex gap-2 items-end">
          <input
            ref={fileInput}
            type="file"
            multiple
            hidden
            accept={ASSISTANT_EXTS.map((x) => "." + x).join(",")}
            onChange={(e) => {
              addFiles(Array.from(e.target.files ?? []));
              e.target.value = "";
            }}
          />
          <button
            type="button"
            onClick={() => fileInput.current?.click()}
            aria-label={t("assistant.attach")}
            title={t("assistant.attach")}
            className="rounded border border-border px-2.5 py-1.5 text-sm text-muted hover:text-fg transition-colors"
          >
            📎
          </button>
          <textarea
            value={text}
            rows={Math.min(5, Math.max(1, text.split("\n").length))}
            onChange={(e) => setText(e.target.value)}
            onPaste={(e) => {
              const files = Array.from(e.clipboardData.files);
              if (files.length > 0) {
                e.preventDefault();
                addFiles(files);
              }
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                send();
              }
            }}
            placeholder={t("assistant.placeholder")}
            className={`${inputCls} resize-none`}
          />
          {voice.available && (
            <button
              type="button"
              onClick={toggleDictation}
              disabled={dictating === "transcribing" || voice.on}
              aria-label={dictating === "recording" ? t("assistant.stopDictation") : t("assistant.dictate")}
              title={dictating === "recording" ? t("assistant.stopDictation") : t("assistant.dictate")}
              className={`rounded border px-2.5 py-1.5 text-sm transition-colors disabled:opacity-40 ${
                dictating === "recording"
                  ? "border-danger text-danger animate-pulse"
                  : dictating === "transcribing"
                    ? "border-border text-muted animate-pulse"
                    : "border-border text-muted hover:text-fg"
              }`}
            >
              {dictating === "transcribing" ? "…" : dictating === "recording" ? "■" : "🎙"}
            </button>
          )}
          <button onClick={send} disabled={sending || (!text.trim() && picked.length === 0)} className={btnCls}>
            {sending && picked.length > 0 ? "…" : t("assistant.send")}
          </button>
        </div>
        <div className="flex gap-4 text-2xs text-muted">
          <button onClick={() => reset(false)} title={t("assistant.newSessionHint")} className="hover:text-fg">
            {t("assistant.newSession")}
          </button>
          <button onClick={() => reset(true)} className="hover:text-danger">
            {t("assistant.clearHistory")}
          </button>
        </div>
      </div>
    </div>
  );
}

function Bubble({ m }: { m: AssistantMessage }) {
  const { t } = useTranslation();
  const time = new Date(m.ts).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const channel = t(
    { web: "assistant.channelWeb", whatsapp: "assistant.channelWhatsapp", task: "assistant.channelTask", place: "assistant.channelPlace", message: "assistant.channelMessage", session: "assistant.channelSession" }[
      m.channel
    ],
  );
  if (m.role === "event") {
    return (
      <div className="text-center text-2xs text-muted">
        ◆ {m.text} · {time}
      </div>
    );
  }
  const mine = m.role === "user";
  return (
    <div className={`flex ${mine ? "justify-end" : "justify-start"}`}>
      <div
        className={`max-w-[85%] rounded-lg px-3 py-2 text-sm whitespace-pre-wrap break-words ${
          mine ? "bg-accent/15 border border-accent/30" : "bg-panel-2 border border-border"
        }`}
      >
        {m.text}
        {m.attachments?.length > 0 && (
          <div className={`flex flex-wrap gap-2 ${m.text ? "mt-2" : ""}`}>
            {m.attachments.map((a) => (
              <AttachmentTile key={a.id} a={a} />
            ))}
          </div>
        )}
        <div className="mt-1 text-3xs text-muted">
          {time} · {channel}
        </div>
        {m.held && (
          <div className="text-3xs text-muted italic" title={t("assistant.heldHint")}>
            🌙 {t("assistant.held", { time: new Date(m.heldUntil ?? 0).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) })}
          </div>
        )}
      </div>
    </div>
  );
}

// Thumbnail for an image, name + size chip for anything else. Files are served
// by the authenticated API, so plain links work in the signed-in browser.
function AttachmentTile({ a }: { a: AssistantAttachment }) {
  const { t } = useTranslation();
  if (a.expired) {
    return (
      <span className="text-xs text-muted italic border border-dashed border-border rounded px-2 py-1">
        {a.name} · {t("assistant.attachExpired")}
      </span>
    );
  }
  if (a.kind === "image" && a.direction === "out") return <AssistantImage a={a} />;
  if (a.kind === "image") {
    return (
      <a href={assistantAttachmentUrl(a.id)} target="_blank" rel="noreferrer" title={`${a.name} · ${fmtSize(a.size)}`}>
        <img
          src={assistantAttachmentUrl(a.id)}
          alt={a.name}
          loading="lazy"
          className="max-h-48 max-w-full rounded border border-border object-cover"
        />
      </a>
    );
  }
  return (
    <a
      href={assistantAttachmentUrl(a.id, a.mime !== "application/pdf")}
      target="_blank"
      rel="noreferrer"
      className="flex items-center gap-2 border border-border rounded bg-bg/40 px-2 py-1.5 text-xs hover:border-accent max-w-full"
    >
      <span className="text-base">📄</span>
      <span className="min-w-0">
        <span className="block truncate font-medium">{a.name}</span>
        <span className="text-muted">{fmtSize(a.size)}</span>
      </span>
    </a>
  );
}

// An image the assistant sent: thumbnail with a loading skeleton and an error
// state, caption underneath, click to enlarge (Escape / backdrop closes) with a
// download link. It is only ever an <img> of the authenticated route: the bytes
// are never interpreted as HTML.
function AssistantImage({ a }: { a: AssistantAttachment }) {
  const { t } = useTranslation();
  const [state, setState] = useState<"loading" | "ok" | "error">("loading");
  const [tries, setTries] = useState(0);
  const [open, setOpen] = useState(false);
  const src = `${assistantAttachmentUrl(a.id)}${tries ? `?r=${tries}` : ""}`;
  const alt = a.caption || t("assistant.imageAlt");
  const ratio = a.width && a.height ? { aspectRatio: `${a.width} / ${a.height}` } : undefined;
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);
  return (
    <figure className="m-0 max-w-full">
      {state === "error" ? (
        <div className="flex items-center gap-2 text-xs text-muted border border-dashed border-border rounded px-2 py-2">
          <span>🖼️ {t("assistant.imageLoadError")}</span>
          <button
            type="button"
            className="underline hover:text-fg"
            onClick={() => {
              setState("loading");
              setTries((n) => n + 1);
            }}
          >
            {t("assistant.imageRetry")}
          </button>
        </div>
      ) : (
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={`${t("assistant.imageOpen")} — ${alt}`}
          title={t("assistant.imageOpen")}
          className={`block relative rounded border border-border overflow-hidden cursor-zoom-in ${state === "loading" ? "animate-pulse bg-bg/60 w-56" : ""}`}
          style={state === "loading" ? (ratio ?? { minHeight: "8rem" }) : undefined}
        >
          <img
            src={src}
            alt={alt}
            loading="lazy"
            decoding="async"
            onLoad={() => setState("ok")}
            onError={() => setState("error")}
            className={`max-h-64 max-w-full sm:max-w-sm object-contain ${state === "loading" ? "opacity-0 absolute inset-0 w-full h-full" : ""}`}
          />
        </button>
      )}
      {a.caption && <figcaption className="mt-1 text-xs text-muted whitespace-pre-wrap">{a.caption}</figcaption>}
      {open && (
        <div
          role="dialog"
          aria-modal="true"
          aria-label={alt}
          className="fixed inset-0 z-50 bg-black/80 flex flex-col items-center justify-center p-4 gap-3"
          onClick={() => setOpen(false)}
        >
          <img src={src} alt={alt} className="max-h-[80vh] max-w-full object-contain rounded" onClick={(e) => e.stopPropagation()} />
          {a.caption && <p className="text-sm text-white/90 max-w-xl text-center">{a.caption}</p>}
          <div className="flex gap-3 text-sm" onClick={(e) => e.stopPropagation()}>
            <a
              href={assistantAttachmentUrl(a.id, true)}
              download={a.name}
              className="px-3 py-1 rounded bg-white/15 text-white hover:bg-white/25"
            >
              {t("assistant.imageDownload")}
            </a>
            <button type="button" autoFocus onClick={() => setOpen(false)} className="px-3 py-1 rounded bg-white/15 text-white hover:bg-white/25">
              {t("assistant.imageClose")}
            </button>
          </div>
        </div>
      )}
    </figure>
  );
}

// ---- Automations ---------------------------------------------------------

function Automations({ projectId }: { projectId: string }) {
  return (
    <div className="flex-1 min-h-0 overflow-auto p-4 space-y-8">
      <Schedules projectId={projectId} />
      <Places />
      <ModelSettings />
      <PushSettings />
      <NightSettings />
      <BatchSettings />
    </div>
  );
}

const ASSISTANT_MODELS = ["sonnet", "opus", "haiku", ""];

function ModelSettings() {
  const { t } = useTranslation();
  const [st, setSt] = useState<AssistantState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [applying, setApplying] = useState(false);
  const refresh = () => api.assistantState().then(setSt).catch(() => {});
  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 5000);
    return () => clearInterval(id);
  }, []);
  if (!st) return null;
  const label = (m: string) => (m ? t(`configuration.model_${m}`) : t("assistant.modelHub"));
  function choose(m: string) {
    setError(null);
    api.assistantSetModel(m).then(setSt).catch((e) => setError(e.message));
  }
  async function applyNow() {
    setApplying(true);
    setError(null);
    try {
      await api.assistantReset(false);
      await refresh();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setApplying(false);
    }
  }
  return (
    <section className="space-y-2">
      <h2 className="font-semibold">{t("assistant.modelTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.modelIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      <select
        value={st.model}
        onChange={(e) => choose(e.target.value)}
        className="bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
      >
        {ASSISTANT_MODELS.map((m) => (
          <option key={m || "hub"} value={m}>{label(m)}</option>
        ))}
      </select>
      <p className="text-xs text-muted">
        {st.modelEffective
          ? t("assistant.modelCurrent", { model: st.modelEffective })
          : t("assistant.modelNoSession")}
      </p>
      {st.modelPending && (
        <div className="flex items-center gap-2 text-xs text-muted">
          <span>{t("assistant.modelPending")}</span>
          <button
            className="px-2 py-1 border border-border rounded hover:border-accent-dim disabled:opacity-50"
            disabled={st.busy || applying}
            onClick={applyNow}
          >
            {applying ? t("assistant.modelApplying") : t("assistant.modelApply")}
          </button>
        </div>
      )}
    </section>
  );
}

function PushSettings() {
  const { t } = useTranslation();
  const [push, setPush] = useState(true);
  const [pushWa, setPushWa] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api.assistantState().then((s) => { setPush(s.push); setPushWa(s.pushWhatsapp); }).catch(() => {});
  }, []);
  function save(p: boolean, w: boolean) {
    setPush(p);
    setPushWa(w);
    api.assistantSetPush(p, w).catch((e) => setError(e.message));
  }
  return (
    <section className="space-y-2">
      <h2 className="font-semibold">{t("assistant.pushTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.pushIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={push} onChange={(e) => save(e.target.checked, pushWa)} />
        {t("assistant.pushOn")}
      </label>
      <label className={`flex items-center gap-2 text-sm ${push ? "" : "opacity-50"}`}>
        <input type="checkbox" checked={pushWa} disabled={!push} onChange={(e) => save(push, e.target.checked)} />
        {t("assistant.pushWhatsapp")}
      </label>
    </section>
  );
}

function NightSettings() {
  const { t } = useTranslation();
  const [st, setSt] = useState<AssistantState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    api.assistantState().then(setSt).catch(() => {});
  }, []);
  if (!st || st.nightMode === undefined) return null;
  function save(patch: AssistantNightPatch) {
    setSaving(true);
    setError(null);
    api
      .assistantSetNight(patch)
      .then(setSt)
      .catch((e) => setError(e.message))
      .finally(() => setSaving(false));
  }
  const on = st.nightMode;
  const field = "bg-bg border border-border rounded px-2 py-1 text-sm";
  const time = (label: string, key: "nightStart" | "nightEnd" | "nightDeliver") => (
    <label className="flex flex-col gap-0.5 text-xs text-muted">
      {label}
      <input type="time" className={field} disabled={!on || saving} value={st[key] ?? ""}
        onChange={(e) => e.target.value && save({ [key]: e.target.value })} />
    </label>
  );
  return (
    <section className="space-y-2">
      <h2 className="font-semibold">{t("assistant.nightTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.nightIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={on} disabled={saving} onChange={(e) => save({ nightMode: e.target.checked })} />
        {t("assistant.nightOn")}
      </label>
      <div className={`flex flex-wrap items-end gap-3 ${on ? "" : "opacity-50"}`}>
        {time(t("assistant.nightStart"), "nightStart")}
        {time(t("assistant.nightEnd"), "nightEnd")}
        {time(t("assistant.nightDeliver"), "nightDeliver")}
        <label className="flex flex-col gap-0.5 text-xs text-muted">
          {t("assistant.nightIdle")}
          <input type="number" min={1} max={720} className={`${field} w-20`} disabled={!on || saving}
            defaultValue={st.nightIdleMinutes}
            onBlur={(e) => {
              const n = Number(e.target.value);
              if (n && n !== st.nightIdleMinutes) save({ nightIdleMinutes: n });
            }} />
        </label>
        <label className="flex flex-col gap-0.5 text-xs text-muted">
          {t("assistant.nightTimezone")}
          <input className={`${field} w-44`} disabled={!on || saving} defaultValue={st.nightTimezone}
            onBlur={(e) => {
              const v = e.target.value.trim();
              if (v && v !== st.nightTimezone) save({ nightTimezone: v });
            }} />
        </label>
      </div>
      {(st.nightHeld ?? 0) > 0 && <p className="text-xs text-muted">🌙 {t("assistant.nightHeld", { count: st.nightHeld })}</p>}
    </section>
  );
}

function BatchSettings() {
  const { t } = useTranslation();
  const [st, setSt] = useState<AssistantState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    api.assistantState().then(setSt).catch(() => {});
  }, []);
  if (!st || st.batchWakeups === undefined) return null;
  function save(patch: AssistantNightPatch) {
    setSaving(true);
    setError(null);
    api
      .assistantSetNight(patch)
      .then(setSt)
      .catch((e) => setError(e.message))
      .finally(() => setSaving(false));
  }
  const on = st.batchWakeups;
  return (
    <section className="space-y-2">
      <h2 className="font-semibold">{t("assistant.batchTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.batchIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={on} disabled={saving} onChange={(e) => save({ batchWakeups: e.target.checked })} />
          {t("assistant.batchOn")}
        </label>
        <label className={`flex items-center gap-1 text-xs text-muted ${on ? "" : "opacity-50"}`}>
          {t("assistant.batchMinutes")}
          <input type="number" min={1} max={120} className="bg-bg border border-border rounded px-2 py-1 text-sm w-20"
            disabled={!on || saving} defaultValue={st.batchMinutes}
            onBlur={(e) => {
              const n = Number(e.target.value);
              if (n && n !== st.batchMinutes) save({ batchMinutes: n });
            }} />
        </label>
      </div>
      {(st.batchHeld ?? 0) > 0 && <p className="text-xs text-muted">⏳ {t("assistant.batchHeld", { count: st.batchHeld })}</p>}
    </section>
  );
}

// Local HH:MM + day set -> UTC 5-field cron (the scheduler runs in UTC).
function toUtcCron(time: string, days: "all" | "week" | "weekend"): string {
  const [h, m] = time.split(":").map(Number);
  const d = new Date();
  d.setHours(h, m, 0, 0);
  const shift = Math.round(
    (Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()) -
      Date.UTC(d.getFullYear(), d.getMonth(), d.getDate())) /
      86400000,
  );
  // A UTC date that moved to the next/previous day shifts the weekday set too.
  const base = { all: [0, 1, 2, 3, 4, 5, 6], week: [1, 2, 3, 4, 5], weekend: [0, 6] }[days];
  const dow = base.map((x) => (x + shift + 7) % 7).sort();
  const field = days === "all" ? "*" : dow.join(",");
  return `${d.getUTCMinutes()} ${d.getUTCHours()} * * ${field}`;
}

// UTC cron back to a local "HH:MM · days" label (best effort for our own crons).
function describeCron(cron: string): string {
  const [min, hour, , , dow] = cron.split(" ");
  if (!/^\d+$/.test(min) || !/^\d+$/.test(hour)) return cron;
  const d = new Date();
  d.setUTCHours(Number(hour), Number(min), 0, 0);
  const hhmm = d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  return dow === "*" ? hhmm : `${hhmm} · ${cron}`;
}

function Schedules({ projectId }: { projectId: string }) {
  const { t } = useTranslation();
  const [tasks, setTasks] = useState<ScheduledTask[]>([]);
  const [name, setName] = useState("");
  const [prompt, setPrompt] = useState("");
  const [time, setTime] = useState("07:30");
  const [days, setDays] = useState<"all" | "week" | "weekend">("all");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(
    () => api.listTasks(projectId).then(setTasks).catch((e) => setError(e.message)),
    [projectId],
  );
  useEffect(() => {
    load();
    const timer = setInterval(load, 10000);
    return () => clearInterval(timer);
  }, [load]);

  async function add() {
    setBusy(true);
    setError(null);
    try {
      await api.createTask({
        name: name.trim(),
        projectId,
        prompt: prompt.trim(),
        schedule: toUtcCron(time, days),
        enabled: true,
        permissionMode: "bypassPermissions",
        model: "",
        command: "",
      });
      setName("");
      setPrompt("");
      await load();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="space-y-3">
      <h2 className="font-semibold">{t("assistant.scheduleTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.scheduleIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      {tasks.length === 0 && <p className="text-sm text-muted">{t("assistant.noTasks")}</p>}
      <ul className="space-y-2">
        {tasks.map((task) => (
          <li key={task.id} className="border border-border rounded-lg p-3 bg-panel text-sm">
            <div className="flex items-center gap-3 flex-wrap">
              <span className="font-medium">{task.name}</span>
              <span className="text-muted mono text-xs">{describeCron(task.schedule)}</span>
              <span className="flex-1" />
              <button
                onClick={() => api.updateTask(task.id, { ...task, enabled: !task.enabled }).then(load)}
                className={task.enabled ? "text-ok text-xs" : "text-muted text-xs"}
              >
                {task.enabled ? t("assistant.enabled") : t("assistant.disabled")}
              </button>
              <button onClick={() => api.runTaskNow(task.id).then(load)} className="text-accent text-xs hover:underline">
                {t("assistant.run")}
              </button>
              <button
                onClick={() =>
                  confirm(t("assistant.confirmDelete", { name: task.name })) && api.deleteTask(task.id).then(load)
                }
                className="text-danger text-xs hover:underline"
              >
                {t("assistant.delete")}
              </button>
            </div>
            <div className="text-xs text-muted mt-1 truncate" title={task.prompt}>
              {task.prompt}
            </div>
          </li>
        ))}
      </ul>
      <div className="border border-border rounded-lg p-3 bg-panel-2 space-y-2">
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          <input className={inputCls} placeholder={t("assistant.taskName")} value={name} onChange={(e) => setName(e.target.value)} />
          <div className="flex items-center gap-1" aria-label={t("assistant.taskTime")}>
            <select className={inputCls} value={time.split(":")[0]} onChange={(e) => setTime(`${e.target.value}:${time.split(":")[1]}`)} aria-label={t("assistant.taskTime")}>
              {Array.from({ length: 24 }, (_, i) => String(i).padStart(2, "0")).map((h) => (
                <option key={h} value={h}>{h}</option>
              ))}
            </select>
            <span>:</span>
            <select className={inputCls} value={time.split(":")[1]} onChange={(e) => setTime(`${time.split(":")[0]}:${e.target.value}`)} aria-label={t("assistant.taskTime")}>
              {Array.from({ length: 12 }, (_, i) => String(i * 5).padStart(2, "0")).map((m) => (
                <option key={m} value={m}>{m}</option>
              ))}
            </select>
          </div>
          <select className={inputCls} value={days} onChange={(e) => setDays(e.target.value as typeof days)} aria-label={t("assistant.taskDays")}>
            <option value="all">{t("assistant.daysAll")}</option>
            <option value="week">{t("assistant.daysWeek")}</option>
            <option value="weekend">{t("assistant.daysWeekend")}</option>
          </select>
        </div>
        <textarea
          className={inputCls}
          rows={2}
          placeholder={t("assistant.taskPromptHint")}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />
        <button className={btnCls} disabled={busy || !name.trim() || !prompt.trim() || !time} onClick={add}>
          {t("assistant.add")}
        </button>
      </div>
    </section>
  );
}

function Places() {
  const { t } = useTranslation();
  const [triggers, setTriggers] = useState<AssistantTrigger[]>([]);
  const [persons, setPersons] = useState<HaEntity[]>([]);
  const [zones, setZones] = useState<HaEntity[]>([]);
  const [person, setPerson] = useState("");
  const [name, setName] = useState("");
  const [place, setPlace] = useState("home");
  const [event, setEvent] = useState<"enter" | "leave">("enter");
  const [prompt, setPrompt] = useState("");
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => api.assistantTriggers().then(setTriggers).catch((e) => setError(e.message)), []);
  useEffect(() => {
    load();
    api.assistantState().then((s) => setPerson(s.person)).catch(() => {});
    api.assistantEntities("person").then(setPersons).catch(() => {});
    api.assistantEntities("zone").then(setZones).catch(() => {});
  }, [load]);

  async function add() {
    setError(null);
    try {
      await api.assistantCreateTrigger({ name: name.trim(), place: place.trim(), event, prompt: prompt.trim() });
      setName("");
      setPrompt("");
      await load();
    } catch (e: any) {
      setError(e.message);
    }
  }

  const when = (ms: number) => (ms ? new Date(ms).toLocaleString(undefined, { dateStyle: "short", timeStyle: "short", hourCycle: "h23" }) : t("assistant.never"));

  return (
    <section className="space-y-3">
      <h2 className="font-semibold">{t("assistant.triggerTitle")}</h2>
      <p className="text-xs text-muted">{t("assistant.triggerIntro")}</p>
      {error && <div className="text-danger text-xs">{error}</div>}
      <label className="block text-xs text-muted">
        {t("assistant.person")}
        <select
          className={`${inputCls} mt-1`}
          value={person}
          onChange={(e) => {
            setPerson(e.target.value);
            api.assistantSetPerson(e.target.value).catch((er) => setError(er.message));
          }}
        >
          <option value="">{t("assistant.personNone")}</option>
          {persons.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name || p.id} ({p.state})
            </option>
          ))}
        </select>
      </label>
      {triggers.length === 0 && <p className="text-sm text-muted">{t("assistant.noTriggers")}</p>}
      <ul className="space-y-2">
        {triggers.map((tr) => (
          <li key={tr.id} className="border border-border rounded-lg p-3 bg-panel text-sm">
            <div className="flex items-center gap-3 flex-wrap">
              <span className="font-medium">{tr.name}</span>
              <span className="text-muted text-xs">
                {tr.event === "message"
                  ? tr.place
                    ? t("assistant.messageFrom", { from: tr.place })
                    : t("assistant.messageFromAnyone")
                  : `${t(tr.event === "enter" ? "assistant.enter" : "assistant.leave")} · ${tr.place}`}
              </span>
              <span className="text-2xs text-muted">{t("assistant.lastFired", { when: when(tr.lastFired) })}</span>
              <span className="flex-1" />
              <button
                onClick={() => api.assistantUpdateTrigger(tr.id, { enabled: !tr.enabled }).then(load)}
                className={tr.enabled ? "text-ok text-xs" : "text-muted text-xs"}
              >
                {tr.enabled ? t("assistant.enabled") : t("assistant.disabled")}
              </button>
              <button onClick={() => api.assistantFireTrigger(tr.id)} className="text-accent text-xs hover:underline">
                {t("assistant.test")}
              </button>
              <button
                onClick={() =>
                  confirm(t("assistant.confirmDelete", { name: tr.name })) && api.assistantDeleteTrigger(tr.id).then(load)
                }
                className="text-danger text-xs hover:underline"
              >
                {t("assistant.delete")}
              </button>
            </div>
            <div className="text-xs text-muted mt-1 truncate" title={tr.prompt}>
              {tr.prompt}
            </div>
          </li>
        ))}
      </ul>
      <div className="border border-border rounded-lg p-3 bg-panel-2 space-y-2">
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
          <input className={inputCls} placeholder={t("assistant.triggerName")} value={name} onChange={(e) => setName(e.target.value)} />
          <select className={inputCls} value={event} onChange={(e) => setEvent(e.target.value as typeof event)} aria-label={t("assistant.triggerEvent")}>
            <option value="enter">{t("assistant.enter")}</option>
            <option value="leave">{t("assistant.leave")}</option>
          </select>
          <div>
            <input
              className={inputCls}
              list="assistant-zones"
              placeholder={t("assistant.triggerPlace")}
              title={t("assistant.triggerPlaceHint")}
              value={place}
              onChange={(e) => setPlace(e.target.value)}
            />
            <datalist id="assistant-zones">
              {zones.map((z) => (
                <option key={z.id} value={(z.name || z.id.replace("zone.", "")).toLowerCase()} />
              ))}
            </datalist>
          </div>
        </div>
        <textarea
          className={inputCls}
          rows={2}
          placeholder={t("assistant.triggerPromptHint")}
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />
        <button className={btnCls} disabled={!name.trim() || !place.trim() || !prompt.trim()} onClick={add}>
          {t("assistant.add")}
        </button>
      </div>
    </section>
  );
}
