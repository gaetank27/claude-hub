import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { SessionSocket, type SendPayload } from "../lib/ws";
import { api } from "../lib/api";
import { VoiceBar, useVoiceMode } from "./VoiceBar";
import { Composer } from "./Composer";
import { PlanPanel } from "./PlanPanel";
import { QuestionPanel } from "./QuestionPanel";

// firstLine returns a compact single-line preview for a collapsed block's
// summary: first line, trimmed, capped so it never wraps.
function firstLine(s: string, max = 80): string {
  const line = (s ?? "").trim().split("\n")[0] ?? "";
  return line.length > max ? line.slice(0, max) + "…" : line;
}

// CollapsibleBlock renders a tool-use / tool-result entry collapsed by default,
// expandable on click via a native <details> (no JS). The summary stays compact
// (label + one-line preview); the full body shows only when expanded. These
// blocks are mechanical "not addressed to the user" noise, hence folded.
function CollapsibleBlock({
  summary,
  preview,
  className = "border-border",
  children,
}: {
  summary: ReactNode;
  preview?: string;
  className?: string;
  children: ReactNode;
}) {
  return (
    <details className={`mono text-xs bg-bg border rounded ${className}`}>
      <summary className="cursor-pointer select-none px-3 py-2 flex items-center gap-2">
        {summary}
        {preview && <span className="text-muted/70 truncate">{preview}</span>}
      </summary>
      <div className="px-3 pb-2 border-t border-border pt-2">{children}</div>
    </details>
  );
}

type Entry =
  | { kind: "init"; model?: string; cwd?: string }
  | { kind: "assistant-text"; text: string; ts?: number }
  | { kind: "tool-use"; name: string; input: unknown }
  | { kind: "tool-result"; text: string; isError?: boolean }
  | { kind: "user-text"; text: string; ts?: number; images?: { mediaType: string; data: string }[] }
  | { kind: "plan"; text: string; planFilePath?: string }
  | { kind: "question"; questions: any[] }
  | { kind: "result"; cost?: number; usage?: any; isError?: boolean };

// Stamp renders a message's date and time in small muted text (nothing for old
// journal entries recorded before timestamps existed).
function Stamp({ ts }: { ts?: number }) {
  if (!ts) return null;
  const d = new Date(ts);
  return (
    <div className="text-[10px] text-muted mt-1 text-right">
      {d.toLocaleDateString()} {d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" })}
    </div>
  );
}

type Totals = { cost: number; input: number; output: number };

// aggregateUsage sums cost and tokens across the session's result events.
// Tokens are per-turn, so they sum directly. total_cost_usd, however, is
// cumulative within a single CLI run and resets to ~0 on every `claude --resume`
// (a resumed session shows several rising-then-dropping segments), so summing it
// raw would double-count: instead we add the per-run delta, taking the full
// value whenever it drops below the previous one (a new run began).
function aggregateUsage(entries: Entry[]): Totals {
  let cost = 0,
    prevCost = 0,
    input = 0,
    output = 0;
  for (const e of entries) {
    if (e.kind !== "result") continue;
    if (typeof e.cost === "number") {
      cost += e.cost >= prevCost ? e.cost - prevCost : e.cost;
      prevCost = e.cost;
    }
    if (e.usage) {
      input += e.usage.input_tokens ?? 0;
      output += e.usage.output_tokens ?? 0;
    }
  }
  return { cost, input, output };
}

// contextOf returns the model's context-window occupancy for an assistant
// message: the prompt size of that single API call (uncached input + cache
// writes + cache reads). It is read from the per-message `usage`, NOT the
// `result` usage, which aggregates the whole turn's calls (so its cache_read can
// far exceed the window). Non-assistant events return null.
function contextOf(ev: any): number | null {
  if (ev?.type !== "assistant") return null;
  const u = ev.message?.usage;
  if (!u) return null;
  return (u.input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0) + (u.cache_read_input_tokens ?? 0);
}

// fmtTokens renders a token count compactly: "261k" above 1000, the raw number
// below.
function fmtTokens(n: number): string {
  return n >= 1000 ? `${Math.round(n / 1000)}k` : String(n);
}

// Decode a stream-json event into zero or more transcript entries.
function decode(ev: any): Entry[] {
  const ts: number | undefined = typeof ev?._ts === "number" ? ev._ts : undefined;
  switch (ev?.type) {
    case "system":
      if (ev.subtype === "init")
        return [{ kind: "init", model: ev.model, cwd: ev.cwd }];
      return [];
    case "assistant": {
      const content = ev.message?.content ?? [];
      const out: Entry[] = [];
      for (const block of content) {
        if (block.type === "text" && block.text)
          out.push({ kind: "assistant-text", text: block.text, ts });
        else if (block.type === "tool_use" && block.name === "ExitPlanMode")
          out.push({
            kind: "plan",
            text: block.input?.plan ?? "",
            planFilePath: block.input?.planFilePath,
          });
        // mcp__ask__ask_user is the hub's blocking replacement for the built-in
        // AskUserQuestion (kept for old journals); both carry { questions }.
        else if (
          block.type === "tool_use" &&
          (block.name === "mcp__ask__ask_user" || block.name === "AskUserQuestion")
        )
          out.push({ kind: "question", questions: block.input?.questions ?? [] });
        else if (block.type === "tool_use")
          out.push({ kind: "tool-use", name: block.name, input: block.input });
      }
      return out;
    }
    case "user": {
      const content = ev.message?.content;
      if (typeof content === "string")
        return [{ kind: "user-text", text: stripVoiceHint(content), ts }];
      const out: Entry[] = [];
      // Collect the message's text and image blocks into a single user-text
      // entry (a pasted prompt carries text + images together); tool_results
      // stay separate.
      const texts: string[] = [];
      const images: { mediaType: string; data: string }[] = [];
      for (const block of content ?? []) {
        if (block.type === "tool_result") {
          // Drop the auto-deny artifact of ExitPlanMode in headless mode: the
          // CLI returns an errored "Exit plan mode?" tool_result. The plan is
          // already rendered from the ExitPlanMode tool_use; this is just noise.
          if (block.content === "Exit plan mode?" || block.content === "Answer questions?")
            continue;
          const text =
            typeof block.content === "string"
              ? block.content
              : JSON.stringify(block.content);
          out.push({ kind: "tool-result", text, isError: block.is_error });
        } else if (block.type === "text") {
          texts.push(block.text);
        } else if (block.type === "image" && block.source?.data) {
          // Anthropic content-block shape echoed back into the journal (see
          // streamjson.UserInputBlocks): base64 data + media type on source.
          images.push({
            mediaType: block.source.media_type ?? "image/png",
            data: block.source.data,
          });
        }
      }
      if (texts.length || images.length)
        out.push({
          kind: "user-text",
          text: stripVoiceHint(texts.join("\n")),
          ts,
          images: images.length ? images : undefined,
        });
      return out;
    }
    case "result":
      return [{ kind: "result", cost: ev.total_cost_usd, usage: ev.usage, isError: ev.is_error }];
    default:
      return []; // stream_event partials and unknowns are ignored
  }
}

// `live` is true for running/starting sessions (live WebSocket + composer) and
// false for finished ones (stopped/error), which are replayed read-only from the
// recorded journal since their runner socket is gone.
// The backend prefixes spoken turns with a "[Mode vocal …]" instruction for the
// model; hide it from the transcript.
const stripVoiceHint = (s: string) => s.replace(/^\[Mode vocal[^\]]*\]\n?/, "");

export function SessionStream({ sessionId, live }: { sessionId: string; live: boolean }) {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<Entry[]>([]);
  const [context, setContext] = useState(0); // latest context-window occupancy
  const [status, setStatus] = useState<"connecting" | "open" | "closed">("connecting");
  const [thinking, setThinking] = useState(false);
  const sockRef = useRef<SessionSocket | null>(null);
  const bottomRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const atBottomRef = useRef(true); // user is pinned to the bottom (default at mount)
  const [showJump, setShowJump] = useState(false); // "scroll to bottom" affordance

  // Buffer incoming WS events and flush them in one RAF batch so replay (which
  // sends the full journal in rapid bursts) triggers a single render+scroll
  // instead of one per event. Live messages (seconds apart) flush on the next
  // frame — imperceptible latency, but eliminates the "scroll through history"
  // animation when entering a session.
  const pendingRef = useRef<Entry[]>([]);
  const rafRef = useRef<number>(0);

  useEffect(() => {
    setEntries([]); // reset when switching session, or when a live session ends
    setContext(0);
    pendingRef.current = [];
    cancelAnimationFrame(rafRef.current);

    if (!live) {
      // Finished session: no runner socket to attach to. Replay the journal once.
      setStatus("closed");
      let cancelled = false;
      api
        .getJournal(sessionId)
        .then((text) => {
          if (cancelled) return;
          const evs: Entry[] = [];
          let ctx = 0;
          for (const line of text.split("\n")) {
            if (!line.trim()) continue;
            try {
              const ev = JSON.parse(line);
              evs.push(...decode(ev));
              const c = contextOf(ev);
              if (c !== null) ctx = c;
            } catch {
              /* tolerate malformed/partial line */
            }
          }
          setEntries(evs);
          setContext(ctx);
        })
        .catch(() => {});
      return () => {
        cancelled = true;
      };
    }
    const sock = new SessionSocket(sessionId, {
      onReset: () => {
        // Replay is about to start — discard any buffered events from a previous
        // connection and reset state so the incoming history renders fresh.
        pendingRef.current = [];
        cancelAnimationFrame(rafRef.current);
        setEntries([]);
        setContext(0);
      },
      onStatus: setStatus,
      onEvent: (ev) => {
        // Thinking / context are lightweight scalars — update them immediately
        // so the UI stays responsive during long tool calls.
        const decoded = decode(ev);
        if (decoded.some((e) => e.kind === "result")) setThinking(false);
        // Replayed history also flows through here; the voice hook only speaks
        // while a voice turn it started is in flight, so replay stays silent.
        for (const e of decoded) {
          if (e.kind === "assistant-text") voiceRef.current?.onAssistantText(e.text);
          else if (e.kind === "result") voiceRef.current?.onTurnEnd();
        }
        const c = contextOf(ev);
        if (c !== null) setContext(c);

        if (!decoded.length) return;

        // Accumulate into the buffer, then schedule a single flush. All replay
        // events land within a few ms, so they coalesce into one RAF and one
        // state update → one scroll-to-bottom instead of N animated scrolls.
        pendingRef.current.push(...decoded);
        cancelAnimationFrame(rafRef.current);
        rafRef.current = requestAnimationFrame(() => {
          const batch = pendingRef.current.splice(0);
          if (batch.length) setEntries((prev) => [...prev, ...batch]);
        });
      },
    });
    sockRef.current = sock;
    return () => {
      cancelAnimationFrame(rafRef.current);
      sock.close();
      sockRef.current = null;
    };
  }, [sessionId, live]);

  useLayoutEffect(() => {
    if (!atBottomRef.current) return;
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [entries, thinking]);

  // Track whether the user is pinned to the bottom so streamed events only
  // auto-scroll when they are already there; otherwise reading up-thread gets
  // yanked back down on every event. 50px absorbs smooth-scroll inertia.
  function onScroll() {
    const el = scrollRef.current;
    if (!el) return;
    atBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 50;
    setShowJump(!atBottomRef.current);
  }

  function jumpToBottom() {
    atBottomRef.current = true;
    setShowJump(false);
    // Jump straight to the end. Smooth-scrolling a long transcript animates in
    // visible steps (and can be interrupted by streamed events mid-glide), which
    // is what made the button feel like it scrolled "by stages". Setting
    // scrollTop directly lands at the bottom in one frame.
    const el = scrollRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }

  const voice = useVoiceMode((text, spoken) => send({ text, voice: spoken }));
  const voiceRef = useRef(voice);
  voiceRef.current = voice;

  function send(payload: string | SendPayload) {
    const p: SendPayload = typeof payload === "string" ? { text: payload } : payload;
    sockRef.current?.send(p);
    atBottomRef.current = true; // re-pin: the user expects to see their input + reply
    // Optimistic echo with the real thumbnails so it matches the journal replay
    // (the images carry the same {mediaType, data} the journal echoes back).
    const images = p.images?.map((i) => ({ mediaType: i.mediaType, data: i.data }));
    setEntries((prev) => [...prev, { kind: "user-text", text: p.text, images, ts: Date.now() }]);
    setThinking(true);
  }

  const totals = useMemo(() => aggregateUsage(entries), [entries]);

  return (
    <div className="flex flex-col h-full">
      <div className="relative flex-1 min-h-0">
        <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-auto p-4 space-y-3">
          {entries.map((e, i) => (
            <EntryView key={i} entry={e} sessionId={sessionId} />
          ))}
          {thinking && <div className="text-accent text-sm animate-pulse">{t("session.claudeThinking")}</div>}
          <div ref={bottomRef} />
        </div>
        {showJump && (
          <button
            onClick={jumpToBottom}
            aria-label={t("session.jumpToLatest")}
            className="absolute bottom-3 right-3 z-10 w-9 h-9 rounded-full bg-accent text-bg shadow-lg hover:bg-accent-dim transition-colors"
          >
            ↓
          </button>
        )}
      </div>
      {live ? (
        <div className="border-t border-border p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))]">
          <div className="text-[11px] mb-2 flex items-center justify-between gap-2">
            <StatusDot status={status} />
            <UsageTotals totals={totals} context={context} />
          </div>
          <VoiceBar v={voice} />
          <Composer
            disabled={status !== "open"}
            onSend={send}
            onUpload={(files) => api.uploadAttachments(sessionId, files)}
          />
        </div>
      ) : (
        <div className="border-t border-border p-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] text-xs text-muted flex items-center justify-between gap-2">
          <span>{t("session.sessionEndedReadOnly")}</span>
          <UsageTotals totals={totals} context={context} />
        </div>
      )}
    </div>
  );
}

function EntryView({
  entry,
  sessionId,
}: {
  entry: Entry;
  sessionId: string;
}) {
  const { t } = useTranslation();
  switch (entry.kind) {
    case "plan":
      return (
        <PlanPanel sessionId={sessionId} plan={entry.text} planFilePath={entry.planFilePath} />
      );
    case "question":
      return <QuestionPanel sessionId={sessionId} questions={entry.questions} />;
    case "init":
      return (
        <div className="mono text-xs text-muted">
          {t("session.sessionStarted")} · {entry.model ?? "claude"} · {entry.cwd}
        </div>
      );
    case "user-text":
      return (
        <div className="bg-panel-2 border-l-2 border-accent rounded px-3 py-2 text-sm">
          {entry.text && <div className="whitespace-pre-wrap">{entry.text}</div>}
          <Stamp ts={entry.ts} />
          {entry.images && entry.images.length > 0 && (
            <div className="flex flex-wrap gap-2 mt-1">
              {entry.images.map((img, i) => (
                <img
                  key={i}
                  src={`data:${img.mediaType};base64,${img.data}`}
                  alt={t("session.imageAlt")}
                  className="h-16 w-16 object-cover rounded border border-border"
                />
              ))}
            </div>
          )}
        </div>
      );
    case "assistant-text":
      return (
        <div className="bg-panel border border-border rounded px-3 py-2 text-sm whitespace-pre-wrap">
          {entry.text}
          <Stamp ts={entry.ts} />
        </div>
      );
    case "tool-use": {
      const input = JSON.stringify(entry.input, null, 2);
      return (
        <CollapsibleBlock
          summary={<span className="text-accent">⚙ {entry.name}</span>}
          preview={firstLine(JSON.stringify(entry.input))}
        >
          <pre className="text-muted overflow-auto">{input}</pre>
        </CollapsibleBlock>
      );
    }
    case "tool-result":
      return (
        <CollapsibleBlock
          className={entry.isError ? "border-danger/50" : "border-border"}
          summary={
            <span className={entry.isError ? "text-danger" : "text-muted"}>
              {entry.isError ? t("session.toolResultError") : t("session.toolResult")}
            </span>
          }
          preview={firstLine(entry.text)}
        >
          <pre
            className={`whitespace-pre-wrap overflow-auto max-h-96 ${
              entry.isError ? "text-danger" : "text-muted"
            }`}
          >
            {entry.text}
          </pre>
        </CollapsibleBlock>
      );
    case "result":
      return (
        <div className="mono text-xs text-muted border-t border-border pt-2">
          {entry.isError ? (
            <span className="text-danger">{t("session.turnError")}</span>
          ) : (
            <span>
              {t("session.turnDone")}
              {entry.usage &&
                t("session.turnUsage", {
                  input: entry.usage.input_tokens ?? 0,
                  output: entry.usage.output_tokens ?? 0,
                })}
              {typeof entry.cost === "number" &&
                t("session.turnCost", { cost: entry.cost.toFixed(4) })}
            </span>
          )}
        </div>
      );
  }
}

// UsageTotals shows the session's cumulative cost and tokens (summed from the
// result events, see aggregateUsage) plus the current context-window occupancy
// (a snapshot of the latest turn, see contextOf). Hidden until there is
// something to show.
function UsageTotals({ totals, context }: { totals: Totals; context: number }) {
  const { t } = useTranslation();
  if (totals.input === 0 && totals.output === 0 && totals.cost === 0) return null;
  return (
    <span className="mono text-muted shrink-0" title={t("session.usageTotalsTitle")}>
      {t("session.usageTotals", {
        input: totals.input,
        output: totals.output,
        cost: totals.cost.toFixed(4),
      })}
      {context > 0 && t("session.usageContext", { context: fmtTokens(context) })}
    </span>
  );
}

function StatusDot({ status }: { status: "connecting" | "open" | "closed" }) {
  const { t } = useTranslation();
  const map = {
    open: { c: "text-ok", label: t("session.statusConnected") },
    connecting: { c: "text-danger", label: t("session.statusConnecting") },
    closed: { c: "text-danger", label: t("session.statusReconnecting") },
  }[status];
  return <span className={`mono ${map.c}`}>● {map.label}</span>;
}
