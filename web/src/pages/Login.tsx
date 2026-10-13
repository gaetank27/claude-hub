import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";

const input =
  "bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none";
const button =
  "bg-accent text-bg font-medium rounded px-4 py-2 text-sm w-full disabled:opacity-40 hover:bg-accent-dim transition-colors";

// AuthShell centers a single card on a bare page — the chrome (sidebar, tab bar)
// is intentionally absent: the operator is not signed in yet.
export function AuthShell({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="flex min-h-full items-center justify-center bg-bg p-4">
      <div className="w-full max-w-sm space-y-5 bg-panel border border-border rounded-lg p-6">
        <div className="flex items-center gap-2">
          <span className="mono text-accent font-semibold tracking-wide">◆</span>
          <span className="text-sm font-medium">{title}</span>
        </div>
        {children}
      </div>
    </div>
  );
}

// Login is the local sign-in: password first, then the mandatory TOTP code. When
// OIDC is configured, a button bounces to the server-side OIDC flow instead.
const OIDC_ERRORS: Record<string, string> = {
  denied: "login.oidcErrorDenied",
  expired: "login.oidcErrorExpired",
  state: "login.oidcErrorState",
  exchange: "login.oidcErrorExchange",
  token: "login.oidcErrorToken",
  forbidden: "login.oidcErrorForbidden",
};

export function Login({ oidcEnabled }: { oidcEnabled: boolean }) {
  const { t } = useTranslation();
  const [phase, setPhase] = useState<"password" | "totp">("password");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [remember, setRemember] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  // Surface OIDC callback errors (e.g. stale state, provider denial): the backend
  // redirects back to /?oidc_error=<code>. Only known codes map to a message, so
  // a crafted link cannot put arbitrary text on the sign-in page.
  const [oidcErr, setOidcErr] = useState(() => {
    const p = new URLSearchParams(window.location.search).get("oidc_error");
    return p ?? "";
  });
  useEffect(() => {
    if (oidcErr) {
      // Clean the URL so a page refresh doesn't re-show a stale error.
      window.history.replaceState({}, "", "/");
    }
  }, [oidcErr]);

  async function submitPassword(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      const r = await api.localLogin(username.trim(), password, remember);
      if (r.totpRequired) setPhase("totp");
      else window.location.href = "/";
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function submitTotp(e: React.FormEvent) {
    e.preventDefault();
    setErr("");
    setBusy(true);
    try {
      await api.totpVerify(code.trim());
      window.location.href = "/";
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthShell title={t("login.title")}>
      {phase === "password" ? (
        <form key="password" onSubmit={submitPassword} className="space-y-3">
          <input
            className={input}
            placeholder={t("login.usernamePlaceholder")}
            autoFocus
            autoComplete="username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <input
            className={input}
            type="password"
            placeholder={t("login.passwordPlaceholder")}
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
          <label className="flex items-center gap-2 text-xs text-muted cursor-pointer select-none">
            <input
              type="checkbox"
              className="accent-accent"
              checked={remember}
              onChange={(e) => setRemember(e.target.checked)}
            />
            {t("login.remember")}
          </label>
          {err && <p className="text-xs text-danger">{err}</p>}
          <button className={button} disabled={busy || !username || !password}>
            {busy ? "…" : t("login.continue")}
          </button>
        </form>
      ) : (
        <form key="totp" onSubmit={submitTotp} id="mfa-form" name="mfa" className="space-y-3">
          <p className="text-xs text-muted">
            {t("login.totpHint")}
          </p>
          <input
            className={`${input} tracking-[0.4em] text-center`}
            id="totp"
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            autoComplete="one-time-code"
            name="totp"
            aria-label="Two-factor authentication code (TOTP)"
            autoFocus
            maxLength={6}
            placeholder="123456"
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
          />
          {err && <p className="text-xs text-danger">{err}</p>}
          <button className={button} disabled={busy || code.length < 6}>
            {busy ? "…" : t("login.verify")}
          </button>
          <button
            type="button"
            className="text-xs text-muted hover:text-fg transition-colors"
            onClick={() => {
              setPhase("password");
              setCode("");
              setErr("");
            }}
          >
            {t("login.back")}
          </button>
        </form>
      )}

      {oidcEnabled && phase === "password" && (
        <div className="pt-1 border-t border-border space-y-2">
          {oidcErr && (
            <p className="text-xs text-danger bg-danger/10 border border-danger/30 rounded px-3 py-2">
              {t(OIDC_ERRORS[oidcErr] ?? "login.oidcErrorGeneric")}
            </p>
          )}
          <a
            href={remember ? "/auth/login?remember=1" : "/auth/login"}
            className="block text-center text-xs border border-border rounded px-3 py-2 mt-1 hover:border-accent-dim transition-colors"
            onClick={() => setOidcErr("")}
          >
            {t("login.ssoButton")}
          </a>
        </div>
      )}
    </AuthShell>
  );
}
