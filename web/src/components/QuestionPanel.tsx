import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";
import { Button } from "./ui";

type Option = { label: string; description?: string };
type Question = {
  question: string;
  header?: string;
  multiSelect?: boolean;
  options: Option[];
};

// Rendered when Claude calls the hub's blocking mcp__ask__ask_user tool (the
// built-in AskUserQuestion is disabled in headless mode — it would auto-deny).
// The ask-mcp tool call is blocking server-side: it holds the session until the
// user submits here, then POST /answer unblocks it and the answer becomes the
// tool_result. So the session genuinely waits — it never continues on a
// non-answer.
export function QuestionPanel({
  sessionId,
  questions,
}: {
  sessionId: string;
  questions: Question[];
}) {
  const { t } = useTranslation();
  const [picks, setPicks] = useState<string[][]>(() => questions.map(() => []));
  const [other, setOther] = useState<string[]>(() => questions.map(() => ""));
  const [pending, setPending] = useState(false);
  const [sent, setSent] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function toggle(qi: number, label: string) {
    setPicks((prev) => {
      const next = prev.map((a) => [...a]);
      if (questions[qi].multiSelect) {
        const i = next[qi].indexOf(label);
        if (i >= 0) next[qi].splice(i, 1);
        else next[qi].push(label);
      } else {
        next[qi] = next[qi][0] === label ? [] : [label];
      }
      return next;
    });
  }

  const answered = questions.map(
    (_, qi) => picks[qi].length > 0 || other[qi].trim().length > 0,
  );
  const canSubmit = answered.every(Boolean) && !pending && !sent;

  async function submit() {
    const lines = questions.map((q, qi) => {
      const chosen = [...picks[qi]];
      if (other[qi].trim()) chosen.push(other[qi].trim());
      return `${q.header || q.question}: ${chosen.join(", ")}`;
    });
    setPending(true);
    setError(null);
    try {
      await api.answerQuestion(sessionId, lines.join("\n"));
      setSent(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(false);
    }
  }

  return (
    <div className="border border-accent-dim/60 rounded-card overflow-hidden">
      <div className="bg-panel-2 px-3 py-2 border-b border-border">
        <span className="mono text-xs text-accent uppercase tracking-wider">? {t("session.question")}</span>
      </div>
      <div className="px-4 py-3 space-y-4 bg-panel">
        {questions.map((q, qi) => (
          <div key={qi} className="space-y-2">
            <div className="text-sm font-medium">{q.question}</div>
            <div className="flex flex-wrap gap-2">
              {q.options.map((opt) => {
                const selected = picks[qi].includes(opt.label);
                return (
                  <button
                    key={opt.label}
                    onClick={() => !sent && !pending && toggle(qi, opt.label)}
                    disabled={sent || pending}
                    title={opt.description}
                    className={`text-sm rounded-control px-3 py-1.5 border transition-colors disabled:opacity-50 ${
                      selected
                        ? "bg-accent text-bg border-accent"
                        : "border-border text-fg hover:border-accent-dim"
                    }`}
                  >
                    {opt.label}
                  </button>
                );
              })}
            </div>
            <input
              value={other[qi]}
              onChange={(e) =>
                setOther((prev) => prev.map((v, i) => (i === qi ? e.target.value : v)))
              }
              disabled={sent || pending}
              placeholder={t("session.otherAnswer")}
              className="w-full bg-bg border border-border rounded-control px-3 py-1.5 text-sm focus:border-accent-dim outline-none disabled:opacity-50"
            />
          </div>
        ))}
      </div>
      <div className="flex items-center gap-2 px-3 py-2 border-t border-border bg-panel-2">
        {sent ? (
          <span className="mono text-xs text-ok">{t("session.answerSent")}</span>
        ) : (
          <Button size="sm" onClick={submit} disabled={!canSubmit} loading={pending}>
            {t("session.send")}
          </Button>
        )}
        {error && <span className="text-danger text-xs ml-2">{error}</span>}
      </div>
    </div>
  );
}
