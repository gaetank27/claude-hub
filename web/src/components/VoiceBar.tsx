import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";
import {
  Listener,
  Speaker,
  loadReplyMode,
  saveReplyMode,
  transcribe,
  voiceAvailable,
  type ReplyMode,
  type VoiceState,
} from "../lib/voice";

// useVoiceMode drives hands-free conversation: mic → local STT → onSend, and
// (when replies are spoken) assistant text → local TTS. The parent feeds it the
// session events through onAssistantText / onTurnEnd.
export function useVoiceMode(onSend: (text: string, spoken: boolean) => void) {
  const [available, setAvailable] = useState(false);
  // Hands-free mode is opt-in (Configuration → "Mode vocal mains libres"): it
  // needs a server fast enough for local STT. `available` (mic + STT) stays
  // independent so dictation keeps working.
  const [handsFree, setHandsFree] = useState(false);
  const [on, setOn] = useState(false);
  const [state, setState] = useState<VoiceState>("listening");
  const [reply, setReplyState] = useState<ReplyMode>(loadReplyMode);
  const [error, setError] = useState("");

  const listener = useRef<Listener | null>(null);
  const speaker = useRef<Speaker | null>(null);
  const replyRef = useRef(reply);
  const awaiting = useRef(false); // a voice turn is in flight: speak its reply
  const sendRef = useRef(onSend);
  sendRef.current = onSend;
  replyRef.current = reply;

  useEffect(() => {
    voiceAvailable().then(setAvailable);
    api.getSettings().then((s) => setHandsFree(!!s.handsFreeVoice)).catch(() => {});
  }, []);

  const stop = useCallback(() => {
    listener.current?.stop();
    listener.current = null;
    speaker.current?.stop();
    speaker.current = null;
    awaiting.current = false;
    setOn(false);
  }, []);

  useEffect(() => stop, [stop]);

  const start = useCallback(async () => {
    setError("");
    const sp = new Speaker((active) => {
      if (active) setState("speaking");
      else setState(awaiting.current ? "thinking" : "listening");
      if (listener.current) listener.current.threshold = active ? 8 : 3;
    });
    const li = new Listener({
      onSpeechStart: () => {
        // Barge-in: the user talks over the assistant → cut its voice.
        if (sp.active) sp.stop();
        setState("hearing");
      },
      onUtterance: async (blob) => {
        setState("transcribing");
        try {
          const text = await transcribe(blob);
          if (!text) return setState("listening");
          awaiting.current = true;
          setState("thinking");
          sendRef.current(text, replyRef.current === "voice");
        } catch (e: any) {
          setError(String(e?.message ?? e));
          setState("listening");
        }
      },
      onError: (m) => {
        setError(m);
        stop();
      },
    });
    speaker.current = sp;
    listener.current = li;
    setOn(true);
    setState("listening");
    await li.start();
  }, [stop]);

  return {
    available,
    handsFree,
    on,
    state,
    reply,
    error,
    toggle: () => (on ? stop() : start()),
    setReply: (m: ReplyMode) => {
      saveReplyMode(m);
      setReplyState(m);
      if (m === "text") speaker.current?.stop();
    },
    onAssistantText: (text: string) => {
      if (on && awaiting.current && replyRef.current === "voice") speaker.current?.speak(text);
    },
    onTurnEnd: () => {
      awaiting.current = false;
      if (on && !speaker.current?.active) setState("listening");
    },
  };
}

export type VoiceApi = ReturnType<typeof useVoiceMode>;

const STATE_STYLE: Record<VoiceState, string> = {
  listening: "bg-accent/40 animate-pulse",
  hearing: "bg-accent scale-125",
  transcribing: "bg-accent-dim animate-bounce",
  thinking: "bg-accent/60 animate-ping",
  speaking: "bg-ok animate-pulse scale-110",
};

export function VoiceBar({ v }: { v: VoiceApi }) {
  const { t } = useTranslation();
  if (!v.available || !v.handsFree) return null;
  const pill = (m: ReplyMode, label: string) => (
    <button
      onClick={() => v.setReply(m)}
      className={`px-2 py-0.5 rounded-pill text-xs transition-colors ${
        v.reply === m ? "bg-accent text-bg" : "text-muted hover:text-text"
      }`}
    >
      {label}
    </button>
  );
  return (
    <div className="flex items-center gap-2 mb-2 flex-wrap">
      <button
        onClick={v.toggle}
        className={`rounded-control border px-2.5 py-1 text-xs transition-colors ${
          v.on ? "border-accent text-accent" : "border-border text-muted hover:text-text"
        }`}
      >
        {v.on ? t("voice.stop") : t("voice.start")}
      </button>
      {v.on && (
        <>
          <span className={`inline-block w-3 h-3 rounded-full transition-all ${STATE_STYLE[v.state]}`} />
          <span className="text-xs text-muted">{t(`voice.state.${v.state}`)}</span>
        </>
      )}
      <span className="ml-auto flex items-center gap-1 border border-border rounded-pill p-0.5">
        <span className="text-[11px] text-muted pl-1.5">{t("voice.replies")}</span>
        {pill("voice", t("voice.replyVoice"))}
        {pill("text", t("voice.replyText"))}
      </span>
      {v.error && <span className="text-danger text-xs w-full">{v.error === "mic" ? t("voice.micDenied") : v.error}</span>}
    </div>
  );
}
