import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";

const input =
  "bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none";
const button =
  "bg-accent text-bg font-medium rounded px-4 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors";
const label = "block text-xs text-muted mb-1";

type Step = "admin" | "totp" | "options" | "claude" | "finish";

// Setup is the first-boot wizard. It runs only while the backend reports
// needsSetup; once finished it locks server-side and the app reloads signed in.
export function Setup() {
  const { t } = useTranslation();
  const [step, setStep] = useState<Step>("admin");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  // Admin identity (carried across steps; username keys the confirm/finish calls).
  const [username, setUsername] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [password2, setPassword2] = useState("");

  // TOTP enrolment material returned by /setup/admin.
  const [qrPng, setQrPng] = useState("");
  const [secret, setSecret] = useState("");
  const [code, setCode] = useState("");

  // Options.
  const [publicBaseUrl, setPublicBaseUrl] = useState(window.location.origin);
  const [oidcEnabled, setOidcEnabled] = useState(false);
  const [oidc, setOidc] = useState({
    issuer: "",
    clientId: "",
    clientSecret: "",
    redirectUrl: `${window.location.origin}/auth/callback`,
    allowedEmails: "",
  });

  // Claude subscription (optional). OAuth opens an authorize URL and pastes back a
  // code; apikey is one-shot. claudeLogin holds an in-progress OAuth sign-in.
  const [claudeKind, setClaudeKind] = useState<"oauth" | "apikey">("oauth");
  const [claudeLabel, setClaudeLabel] = useState(() => t("setup.kindSubscription"));
  const [claudeApiKey, setClaudeApiKey] = useState("");
  const [claudeLogin, setClaudeLogin] = useState<{ id: string; authUrl: string } | null>(null);
  const [claudeCode, setClaudeCode] = useState("");
  const [claudeDone, setClaudeDone] = useState("");

  const run = async (fn: () => Promise<void>) => {
    setErr("");
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const submitAdmin = (e: React.FormEvent) => {
    e.preventDefault();
    if (password !== password2) {
      setErr(t("setup.passwordsMismatch"));
      return;
    }
    run(async () => {
      const r = await api.setupAdmin(username.trim(), email.trim(), password);
      setQrPng(r.qrPng);
      setSecret(r.secret);
      setStep("totp");
    });
  };

  const submitTotp = (e: React.FormEvent) => {
    e.preventDefault();
    run(async () => {
      await api.setupConfirmTotp(username.trim(), code.trim());
      setStep("options");
    });
  };

  const submitOptions = (e: React.FormEvent) => {
    e.preventDefault();
    run(async () => {
      await api.setupOptions({
        publicBaseUrl: publicBaseUrl.trim(),
        oidc: oidcEnabled ? { enabled: true, ...oidc } : undefined,
      });
      setStep("claude");
    });
  };

  // Claude step. apikey connects in one call; oauth connects (logged-out) then
  // opens an authorize URL whose code is pasted back to finish.
  const submitClaude = (e: React.FormEvent) => {
    e.preventDefault();
    run(async () => {
      if (claudeKind === "apikey") {
        await api.setupConnectClaude(claudeLabel.trim() || t("setup.kindApiKey"), "apikey", claudeApiKey.trim());
        setClaudeDone(t("setup.apiKeyConnected"));
        setStep("finish");
        return;
      }
      const a = await api.setupConnectClaude(claudeLabel.trim() || t("setup.kindSubscription"), "oauth");
      const { authUrl } = await api.setupClaudeLoginStart(a.id);
      setClaudeLogin({ id: a.id, authUrl });
    });
  };

  const confirmClaudeLogin = (e: React.FormEvent) => {
    e.preventDefault();
    if (!claudeLogin) return;
    run(async () => {
      await api.setupClaudeLoginFinish(claudeLogin.id, claudeCode.trim());
      setClaudeDone(t("setup.subscriptionConnected"));
      setClaudeLogin(null);
      setStep("finish");
    });
  };

  const finish = () =>
    run(async () => {
      await api.setupFinish(username.trim());
      window.location.href = "/";
    });

  const steps: Step[] = ["admin", "totp", "options", "claude", "finish"];

  return (
    <div className="flex min-h-full items-center justify-center bg-bg p-4">
      <div className="w-full max-w-md space-y-5 bg-panel border border-border rounded-lg p-6">
        <div className="flex items-center gap-2">
          <span className="mono text-accent font-semibold tracking-wide">◆</span>
          <span className="text-sm font-medium">{t("setup.title")}</span>
        </div>
        <div className="flex gap-1.5">
          {steps.map((s) => (
            <span
              key={s}
              className={`h-1 flex-1 rounded ${
                steps.indexOf(s) <= steps.indexOf(step) ? "bg-accent" : "bg-border"
              }`}
            />
          ))}
        </div>

        {step === "admin" && (
          <form onSubmit={submitAdmin} className="space-y-3">
            <p className="text-xs text-muted">{t("setup.adminIntro")}</p>
            <div>
              <span className={label}>{t("setup.username")}</span>
              <input className={input} autoFocus value={username} onChange={(e) => setUsername(e.target.value)} />
            </div>
            <div>
              <span className={label}>{t("setup.emailOptional")}</span>
              <input className={input} type="email" value={email} onChange={(e) => setEmail(e.target.value)} />
            </div>
            <div>
              <span className={label}>{t("setup.passwordMin")}</span>
              <input
                className={input}
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </div>
            <div>
              <span className={label}>{t("setup.confirmPassword")}</span>
              <input
                className={input}
                type="password"
                value={password2}
                onChange={(e) => setPassword2(e.target.value)}
              />
            </div>
            {err && <p className="text-xs text-danger">{err}</p>}
            <button className={`${button} w-full`} disabled={busy || !username || password.length < 8}>
              {busy ? "…" : t("setup.next")}
            </button>
          </form>
        )}

        {step === "totp" && (
          <form onSubmit={submitTotp} className="space-y-3">
            <p className="text-xs text-muted">
              {t("setup.totpIntro")}
            </p>
            {qrPng && (
              <img
                src={`data:image/png;base64,${qrPng}`}
                alt={t("setup.qrAlt")}
                className="mx-auto rounded bg-white p-2"
                width={196}
                height={196}
              />
            )}
            <p className="text-[11px] text-muted break-all">
              {t("setup.manualKey")} <span className="mono text-fg">{secret}</span>
            </p>
            <input
              className={`${input} tracking-[0.4em] text-center`}
              inputMode="numeric"
              maxLength={6}
              placeholder={t("setup.codePlaceholder")}
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
            />
            {err && <p className="text-xs text-danger">{err}</p>}
            <button className={`${button} w-full`} disabled={busy || code.length < 6}>
              {busy ? "…" : t("setup.confirm2fa")}
            </button>
          </form>
        )}

        {step === "options" && (
          <form onSubmit={submitOptions} className="space-y-3">
            <div>
              <span className={label}>{t("setup.publicBaseUrl")}</span>
              <input
                className={input}
                value={publicBaseUrl}
                onChange={(e) => setPublicBaseUrl(e.target.value)}
                placeholder={t("setup.publicBaseUrlPlaceholder")}
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={oidcEnabled} onChange={(e) => setOidcEnabled(e.target.checked)} />
              {t("setup.enableOidc")}
            </label>
            {oidcEnabled && (
              <div className="space-y-2 border border-accent/40 rounded-lg p-3 bg-panel-2">
                <input
                  className={input}
                  placeholder={t("setup.issuerPlaceholder")}
                  value={oidc.issuer}
                  onChange={(e) => setOidc({ ...oidc, issuer: e.target.value })}
                />
                <input
                  className={input}
                  placeholder={t("setup.clientIdPlaceholder")}
                  value={oidc.clientId}
                  onChange={(e) => setOidc({ ...oidc, clientId: e.target.value })}
                />
                <input
                  className={input}
                  type="password"
                  placeholder={t("setup.clientSecretPlaceholder")}
                  value={oidc.clientSecret}
                  onChange={(e) => setOidc({ ...oidc, clientSecret: e.target.value })}
                />
                <input
                  className={input}
                  placeholder={t("setup.redirectUrlPlaceholder")}
                  value={oidc.redirectUrl}
                  onChange={(e) => setOidc({ ...oidc, redirectUrl: e.target.value })}
                />
                <input
                  className={input}
                  placeholder={t("setup.allowedEmailsPlaceholder")}
                  value={oidc.allowedEmails}
                  onChange={(e) => setOidc({ ...oidc, allowedEmails: e.target.value })}
                />
              </div>
            )}
            {err && <p className="text-xs text-danger">{err}</p>}
            <button className={`${button} w-full`} disabled={busy}>
              {busy ? "…" : t("setup.saveContinue")}
            </button>
          </form>
        )}

        {step === "claude" && (
          <div className="space-y-3">
            <p className="text-xs text-muted">
              {t("setup.claudeIntro")}
            </p>

            {!claudeLogin ? (
              <form onSubmit={submitClaude} className="space-y-3">
                <div className="flex gap-1.5">
                  <button
                    type="button"
                    onClick={() => setClaudeKind("oauth")}
                    className={`flex-1 rounded px-3 py-1.5 text-xs border transition-colors ${
                      claudeKind === "oauth" ? "border-accent text-fg" : "border-border text-muted"
                    }`}
                  >
                    {t("setup.kindSubscription")}
                  </button>
                  <button
                    type="button"
                    onClick={() => setClaudeKind("apikey")}
                    className={`flex-1 rounded px-3 py-1.5 text-xs border transition-colors ${
                      claudeKind === "apikey" ? "border-accent text-fg" : "border-border text-muted"
                    }`}
                  >
                    {t("setup.kindApiKey")}
                  </button>
                </div>
                <div>
                  <span className={label}>{t("common.label")}</span>
                  <input className={input} value={claudeLabel} onChange={(e) => setClaudeLabel(e.target.value)} />
                </div>
                {claudeKind === "apikey" && (
                  <div>
                    <span className={label}>{t("setup.apiKeyLabel")}</span>
                    <input
                      className={input}
                      type="password"
                      placeholder={t("setup.apiKeyPlaceholder")}
                      value={claudeApiKey}
                      onChange={(e) => setClaudeApiKey(e.target.value)}
                    />
                  </div>
                )}
                {err && <p className="text-xs text-danger">{err}</p>}
                <button
                  className={`${button} w-full`}
                  disabled={busy || (claudeKind === "apikey" && !claudeApiKey)}
                >
                  {busy ? "…" : claudeKind === "apikey" ? t("setup.connect") : t("setup.connectSubscription")}
                </button>
                <button
                  type="button"
                  className="block w-full text-center text-xs text-muted hover:text-fg transition-colors"
                  onClick={() => setStep("finish")}
                >
                  {t("setup.skipForNow")}
                </button>
              </form>
            ) : (
              <form onSubmit={confirmClaudeLogin} className="space-y-3">
                <p className="text-xs text-muted">
                  {t("setup.oauthIntro")}
                </p>
                <a
                  href={claudeLogin.authUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="block text-center text-xs border border-accent-dim rounded px-3 py-2 text-accent hover:bg-panel-2 transition-colors break-all"
                >
                  {t("setup.openAuthPage")}
                </a>
                <input
                  className={input}
                  placeholder={t("setup.pasteCodePlaceholder")}
                  value={claudeCode}
                  onChange={(e) => setClaudeCode(e.target.value)}
                />
                {err && <p className="text-xs text-danger">{err}</p>}
                <button className={`${button} w-full`} disabled={busy || !claudeCode}>
                  {busy ? "…" : t("setup.validateContinue")}
                </button>
                <button
                  type="button"
                  className="block w-full text-center text-xs text-muted hover:text-fg transition-colors"
                  onClick={() => {
                    setClaudeLogin(null);
                    setStep("finish");
                  }}
                >
                  {t("setup.skipForNow")}
                </button>
              </form>
            )}
          </div>
        )}

        {step === "finish" && (
          <div className="space-y-3">
            <p className="text-sm">{t("setup.complete")}</p>
            {claudeDone && <p className="text-xs text-accent">{claudeDone}</p>}
            <p className="text-xs text-muted">
              {t("setup.finishHint")}
            </p>
            {err && <p className="text-xs text-danger">{err}</p>}
            <button className={`${button} w-full`} disabled={busy} onClick={finish}>
              {busy ? "…" : t("setup.enterHub")}
            </button>
          </div>
        )}
      </div>
    </div>
  );
}
