import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";
import { Button } from "./ui";

// Rendered when Claude proposes a plan via ExitPlanMode while in plan mode.
// "Valider" resumes the session in bypassPermissions to execute the plan;
// "Refuser" keeps it in plan mode for revisions. The backend endpoint that
// performs the resume-with-mode-switch lands in slice 2 step (2) — until then
// the buttons surface the error gracefully.
export function PlanPanel({
  sessionId,
  plan,
  planFilePath,
}: {
  sessionId: string;
  plan: string;
  planFilePath?: string;
}) {
  const { t } = useTranslation();
  const [pending, setPending] = useState<null | "approve" | "reject">(null);
  const [decided, setDecided] = useState<null | "approve" | "reject">(null);
  const [error, setError] = useState<string | null>(null);

  async function decide(decision: "approve" | "reject") {
    setPending(decision);
    setError(null);
    try {
      await api.decidePlan(sessionId, decision);
      setDecided(decision);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setPending(null);
    }
  }

  return (
    <div className="border border-accent-dim/60 rounded-card overflow-hidden">
      <div className="flex items-center justify-between bg-panel-2 px-3 py-2 border-b border-border">
        <span className="mono text-xs text-accent uppercase tracking-wider">◆ {t("session.planProposed")}</span>
        {planFilePath && (
          <span className="mono text-3xs text-muted truncate ml-3">{planFilePath}</span>
        )}
      </div>
      <pre className="text-sm whitespace-pre-wrap px-4 py-3 max-h-96 overflow-auto bg-panel">
        {plan}
      </pre>
      <div className="flex items-center gap-2 px-3 py-2 border-t border-border bg-panel-2">
        {decided ? (
          <span className={`mono text-xs ${decided === "approve" ? "text-ok" : "text-muted"}`}>
            {decided === "approve" ? t("session.planApproved") : t("session.planRejected")}
          </span>
        ) : (
          <>
            <Button
              size="sm"
              onClick={() => decide("approve")}
              disabled={pending !== null}
              loading={pending === "approve"}
            >
              {pending === "approve" ? t("session.planApproving") : t("session.approveAndExecute")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => decide("reject")}
              disabled={pending !== null}
            >
              {t("session.reject")}
            </Button>
          </>
        )}
        {error && <span className="text-danger text-xs ml-2">{error}</span>}
      </div>
    </div>
  );
}
