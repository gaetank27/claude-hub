import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api, type ClaudeAccount, type Device, type NetworkInfo, type Settings, type VaultEntry } from "../lib/api";
import { McpRemotePanel } from "../components/McpRemotePanel";
import { THEMES, getTheme, setTheme, type ThemeId } from "../lib/theme";

const PERMISSION_MODES = ["default", "acceptEdits", "plan", "bypassPermissions"];

// Claude model aliases offered in the picker. Aliases (not pinned ids) so they
// track the latest version of each tier automatically. Sonnet/Haiku are the
// cheaper tiers; Opus is the default (most capable).
const MODELS = ["opus", "sonnet", "haiku"];

export function Configuration() {
  const { t } = useTranslation();
  const [form, setForm] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<"idle" | "saving" | "saved">("idle");
  const [networkInfo, setNetworkInfo] = useState<NetworkInfo | null>(null);

  useEffect(() => {
    api.getSettings().then(setForm).catch((e) => setError(e.message));
    api.getNetworkInfo().then(setNetworkInfo).catch(() => {});
  }, []);

  function set<K extends keyof Settings>(key: K, value: Settings[K]) {
    setForm((f) => (f ? { ...f, [key]: value } : f));
    setStatus("idle");
  }

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!form) return;
    setStatus("saving");
    setError(null);
    try {
      const saved = await api.updateSettings(form);
      setForm(saved);
      setStatus("saved");
    } catch (e: any) {
      setError(e.message);
      setStatus("idle");
    }
  }

  if (!form) {
    return (
      <div className="p-8 text-muted">
        {error ? <span className="text-danger">{error}</span> : t("common.loading")}
      </div>
    );
  }

  return (
    <div className="p-4 sm:p-8 max-w-2xl">
      <h1 className="text-2xl font-semibold mb-1">{t("configuration.title")}</h1>
      <p className="text-muted text-sm mb-6">
        {t("configuration.intro_pre")}
        <strong>{t("configuration.introStrong")}</strong>
        {t("configuration.intro_post")}
      </p>

      <ThemePicker />

      <ClaudeAccounts />

      <ProjectVault />

      <Devices />
      <McpRemotePanel />

      <form onSubmit={save} className="space-y-5 bg-panel border border-border rounded-lg p-5">
        <Field label={t("configuration.permissionModeLabel")} hint={t("configuration.permissionModeHint")}>
          <select
            value={form.permissionMode}
            onChange={(e) => set("permissionMode", e.target.value)}
            className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
          >
            {PERMISSION_MODES.map((m) => (
              <option key={m} value={m}>{m}</option>
            ))}
          </select>
        </Field>

        <Field label={t("configuration.modelLabel")} hint={t("configuration.modelHint")}>
          <select
            value={form.model}
            onChange={(e) => set("model", e.target.value)}
            className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
          >
            {/* Preserve a custom/pinned id already saved but not in the alias list. */}
            {form.model && !MODELS.includes(form.model) && (
              <option value={form.model}>{form.model}</option>
            )}
            {MODELS.map((m) => (
              <option key={m} value={m}>{t(`configuration.model_${m}`)}</option>
            ))}
          </select>
        </Field>

        <div className="grid grid-cols-2 gap-4">
          <Field label={t("configuration.maxTurnsLabel")} hint={t("configuration.maxTurnsHint")}>
            <input
              type="number"
              min={1}
              value={form.maxTurns}
              onChange={(e) => set("maxTurns", Number(e.target.value))}
              className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
            />
          </Field>
          <Field label={t("configuration.inactivityLabel")} hint={t("configuration.inactivityHint")}>
            <input
              type="number"
              min={1}
              value={form.inactivitySeconds}
              onChange={(e) => set("inactivitySeconds", Number(e.target.value))}
              className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
            />
          </Field>
        </div>

        <div className="grid grid-cols-2 gap-4">
          <Field label={t("configuration.memoryMaxLabel")} hint={t("configuration.memoryMaxHint")}>
            <input
              value={form.memoryMax}
              onChange={(e) => set("memoryMax", e.target.value)}
              placeholder={t("configuration.capPlaceholder")}
              className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
            />
          </Field>
          <Field label={t("configuration.cpuQuotaLabel")} hint={t("configuration.cpuQuotaHint")}>
            <input
              value={form.cpuQuota}
              onChange={(e) => set("cpuQuota", e.target.value)}
              placeholder={t("configuration.capPlaceholder")}
              className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
            />
          </Field>

          <div className="col-span-2 border-t border-border pt-4">
            <label className="flex items-start gap-3 cursor-pointer">
              <input
                type="checkbox"
                checked={form.outputTruncation}
                onChange={(e) => set("outputTruncation", e.target.checked)}
                className="mt-0.5 accent-accent"
              />
              <div>
                <div className="text-sm font-medium">{t("configuration.outputTruncationLabel")}</div>
                <div className="text-xs text-muted mt-0.5">{t("configuration.outputTruncationHint")}</div>
              </div>
            </label>
          </div>

          <div className="col-span-2">
            <label className="flex items-start gap-3 cursor-pointer">
              <input
                type="checkbox"
                checked={!!form.handsFreeVoice}
                onChange={(e) => set("handsFreeVoice", e.target.checked)}
                className="mt-0.5 accent-accent"
              />
              <div>
                <div className="text-sm font-medium">{t("configuration.handsFreeVoiceLabel")}</div>
                <div className="text-xs text-muted mt-0.5">{t("configuration.handsFreeVoiceHint")}</div>
              </div>
            </label>
          </div>
        </div>

        <div className="border-t border-border pt-4 space-y-4">
          <div className="text-sm font-semibold text-muted uppercase tracking-wide">{t("configuration.networkTitle")}</div>
          <Field label={t("configuration.publicBaseUrlLabel")} hint={t("configuration.publicBaseUrlHint")}>
            <input
              value={form.publicBaseUrl ?? ""}
              onChange={(e) => set("publicBaseUrl", e.target.value)}
              placeholder={t("configuration.publicBaseUrlPlaceholder")}
              className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
            />
          </Field>
          <Field label={t("configuration.dnsServerIpLabel")} hint={t("configuration.dnsServerIpHint")}>
            <div className="space-y-2">
              {(
                [
                  { value: "local",  label: t("configuration.dnsIpModeLocal"),  sub: networkInfo?.localIp  || "…" },
                  { value: "public", label: t("configuration.dnsIpModePublic"), sub: networkInfo ? (networkInfo.publicIp || "…") + " — " + t("configuration.dnsIpModePublicHint") : t("configuration.dnsIpModePublicHint") },
                  { value: "custom", label: t("configuration.dnsIpModeCustom"), sub: null },
                ] as { value: string; label: string; sub: string | null }[]
              ).map((opt) => (
                <label key={opt.value} className="flex items-start gap-2 cursor-pointer">
                  <input
                    type="radio"
                    name="dnsServerIpMode"
                    value={opt.value}
                    checked={(form.dnsServerIpMode || "custom") === opt.value}
                    onChange={() => set("dnsServerIpMode", opt.value)}
                    className="mt-0.5 accent-accent"
                  />
                  <div>
                    <span className="text-sm">{opt.label}</span>
                    {opt.sub && <div className="text-xs text-muted">{opt.sub}</div>}
                  </div>
                </label>
              ))}
              {(form.dnsServerIpMode || "custom") === "custom" && (
                <input
                  value={form.dnsServerIp ?? ""}
                  onChange={(e) => set("dnsServerIp", e.target.value)}
                  placeholder={t("configuration.dnsServerIpPlaceholder")}
                  className="bg-bg border border-border rounded px-3 py-2 text-sm w-full focus:border-accent-dim outline-none"
                />
              )}
            </div>
          </Field>
        </div>

        {error && <div className="text-danger text-sm">{error}</div>}

        <div className="flex items-center gap-3">
          <button
            type="submit"
            disabled={status === "saving"}
            className="bg-accent text-bg font-medium rounded px-4 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors"
          >
            {t("common.save")}
          </button>
          {status === "saved" && <span className="text-ok text-sm">{t("configuration.savedMark")}</span>}
        </div>
      </form>
    </div>
  );
}

function AccountLabelEditor({ account, onRenamed }: { account: ClaudeAccount; onRenamed: () => void }) {
  const [value, setValue] = useState(account.label);
  useEffect(() => { setValue(account.label); }, [account.id, account.label]);

  async function save() {
    const trimmed = value.trim();
    if (!trimmed || trimmed === account.label) { setValue(account.label); return; }
    try {
      await api.renameClaudeAccount(account.id, trimmed);
      onRenamed();
    } catch {
      setValue(account.label);
    }
  }

  return (
    <input
      value={value}
      onChange={(e) => setValue(e.target.value)}
      onBlur={save}
      onKeyDown={(e) => { if (e.key === "Enter") (e.target as HTMLInputElement).blur(); }}
      className="font-medium text-sm bg-transparent border-b border-transparent focus:border-accent-dim outline-none"
    />
  );
}

// ClaudeAccounts manages the Claude Code subscriptions the hub runs against:
// disconnect the active one, connect a new one (API key or guided OAuth login),
// and switch between them. Each account is isolated in its own CLAUDE_CONFIG_DIR;
// the dashboard/usage stats follow the active account.
function ClaudeAccounts() {
  const { t } = useTranslation();
  const [accounts, setAccounts] = useState<ClaudeAccount[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // New-account form (subscription / OAuth only).
  const [label, setLabel] = useState("");

  // OAuth sign-in in progress: open the URL, paste back the code.
  const [login, setLogin] = useState<{ id: string; authUrl: string } | null>(null);
  const [code, setCode] = useState("");
  const [loginStep, setLoginStep] = useState<string | null>(null);

  const reload = () => api.listClaudeAccounts().then(setAccounts).catch((e) => setError(e.message));
  useEffect(() => {
    reload();
  }, []);

  async function beginLogin(id: string) {
    setError(null);
    setCode("");
    setBusy(true);
    setLoginStep(t("configuration.loginStepConnecting"));
    try {
      const { authUrl } = await api.startClaudeLogin(id);
      setLogin({ id, authUrl });
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
      setLoginStep(null);
    }
  }

  async function connect(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setLoginStep(t("configuration.loginStepConnecting"));
    try {
      const a = await api.connectClaudeAccount(label.trim(), "oauth");
      setLabel("");
      await reload();
      await beginLogin(a.id);
    } catch (e: any) {
      setError(e.message);
      setBusy(false);
      setLoginStep(null);
    }
  }

  async function finishLogin() {
    if (!login) return;
    setBusy(true);
    setError(null);
    setLoginStep(t("configuration.loginStepValidating"));
    try {
      await api.finishClaudeLogin(login.id, code.trim());
      setLogin(null);
      setCode("");
      await reload();
    } catch (e: any) {
      setError(e.message);
    } finally {
      setBusy(false);
      setLoginStep(null);
    }
  }

  async function act(fn: () => Promise<unknown>) {
    setError(null);
    try {
      await fn();
      await reload();
    } catch (e: any) {
      setError(e.message);
    }
  }

  const switchTo = (id: string) => act(() => api.switchClaudeAccount(id));
  const logout = (id: string) => {
    if (!confirm(t("configuration.confirmLogout"))) return;
    act(() => api.logoutClaudeAccount(id));
  };
  // Delete a subscription for good. The backend stops the account's live sessions
  // and refuses an active account, so for the active one we disconnect first
  // (stops its sessions, clears the active flag) then delete. The seeded "default"
  // account points at the ambient ~/.claude: deleting it only delists it (the dir
  // is preserved and re-detected on next start if no account remains).
  const remove = (a: ClaudeAccount) => {
    const warn = a.active
      ? t("configuration.confirmRemoveActive")
      : a.id === "default"
        ? t("configuration.confirmRemoveDefault")
        : t("configuration.confirmRemove");
    if (!confirm(warn)) return;
    act(async () => {
      if (a.active) await api.logoutClaudeAccount(a.id);
      await api.deleteClaudeAccount(a.id);
    });
  };

  return (
    <section className="bg-panel border border-border rounded-lg p-5 mb-6">
      <h2 className="text-sm font-medium mb-1">{t("configuration.accountsTitle")}</h2>
      <p className="text-xs text-muted mb-4">
        {t("configuration.accountsIntro_pre")}<code>CLAUDE_CONFIG_DIR</code>{t("configuration.accountsIntro_mid")}
        <strong>{t("configuration.accountsIntroStrong")}</strong>{t("configuration.accountsIntro_post")}
      </p>

      {loginStep && (
        <div className="flex items-center gap-2 text-xs text-muted mb-3">
          <span className="inline-block w-3 h-3 border-2 border-accent border-t-transparent rounded-full animate-spin" />
          <span>{loginStep}</span>
        </div>
      )}

      <ul className="space-y-2 mb-5">
        {accounts.map((a) => (
          <li
            key={a.id}
            className={`flex flex-wrap items-center gap-2 rounded-lg border px-3 py-2 ${
              a.active ? "border-accent bg-panel-2" : "border-border"
            }`}
          >
            <AccountLabelEditor account={a} onRenamed={reload} />
            {a.active ? (
              <span className="text-[11px] text-accent border border-accent/40 rounded px-1.5 py-0.5">{t("configuration.badgeDefault")}</span>
            ) : a.status === "loggedout" ? (
              <span className="text-[11px] text-muted border border-border rounded px-1.5 py-0.5">{t("configuration.badgeLoggedOut")}</span>
            ) : null}
            {a.accountEmail && <span className="text-xs text-muted truncate">{a.accountEmail}</span>}
            {a.tokenExpiresAt && a.kind === "oauth" && (
              <span className="text-[11px] text-muted" title={new Date(a.tokenExpiresAt).toLocaleString(undefined, { hourCycle: "h23" })}>
                {t("configuration.tokenExpiresAt", { date: fmtTokenExpiry(a.tokenExpiresAt) })}
              </span>
            )}

            <span className="ml-auto flex items-center gap-2">
              {!a.active && a.status === "connected" && (
                <button
                  type="button"
                  onClick={() => switchTo(a.id)}
                  className="text-xs border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors"
                >
                  {t("configuration.setDefault")}
                </button>
              )}
              {a.status === "loggedout" && a.kind === "oauth" && (
                <button
                  type="button"
                  onClick={() => beginLogin(a.id)}
                  className="text-xs border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors"
                >
                  {t("configuration.signIn")}
                </button>
              )}
              {a.active && (
                <button
                  type="button"
                  onClick={() => logout(a.id)}
                  className="text-xs border border-danger/50 text-danger rounded px-2 py-1 hover:bg-danger/10 transition-colors"
                >
                  {t("configuration.disconnect")}
                </button>
              )}
              <button
                type="button"
                onClick={() => remove(a)}
                className="text-xs text-muted hover:text-danger transition-colors"
                title={t("configuration.deleteTitle")}
              >
                ✕
              </button>
            </span>
          </li>
        ))}
        {accounts.length === 0 && <li className="text-xs text-muted">{t("configuration.noAccounts")}</li>}
      </ul>

      {/* OAuth sign-in, fully in-browser: open the URL, authorize, paste the code. */}
      {login && (
        <div className="border border-accent/40 rounded-lg p-3 mb-5 space-y-3 bg-panel-2">
          <ol className="text-xs text-muted list-decimal list-inside space-y-1">
            <li>
              {t("configuration.oauthStep1Prefix")}
              <a
                href={login.authUrl}
                target="_blank"
                rel="noreferrer"
                className="text-accent underline break-all"
              >
                {t("configuration.oauthStep1Link")}
              </a>
            </li>
            <li>{t("configuration.oauthStep2")}</li>
            <li>{t("configuration.oauthStep3")}</li>
          </ol>
          <div className="flex gap-2">
            <input
              value={code}
              onChange={(e) => setCode(e.target.value)}
              placeholder={t("configuration.codePlaceholder")}
              autoFocus
              className="bg-bg border border-border rounded px-3 py-2 text-sm flex-1 focus:border-accent-dim outline-none"
            />
            <button
              type="button"
              onClick={finishLogin}
              disabled={busy || !code.trim()}
              className="bg-accent text-bg font-medium rounded px-3 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors flex items-center gap-2"
            >
              {busy && <span className="inline-block w-3 h-3 border-2 border-bg border-t-transparent rounded-full animate-spin" />}
              {t("configuration.validate")}
            </button>
            <button
              type="button"
              onClick={() => setLogin(null)}
              className="text-xs text-muted px-2 hover:text-fg transition-colors"
            >
              {t("common.cancel")}
            </button>
          </div>
        </div>
      )}

      {/* Connect a new subscription (Pro/Max via the CLI login). */}
      <form onSubmit={connect} className="border-t border-border pt-4 space-y-3">
        <h3 className="text-xs font-medium">{t("configuration.connectTitle")}</h3>
        <div className="flex flex-wrap gap-2 items-end">
          <input
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            placeholder={t("configuration.labelPlaceholder")}
            className="bg-bg border border-border rounded px-3 py-2 text-sm flex-1 min-w-[12rem] focus:border-accent-dim outline-none"
          />
          <button
            type="submit"
            disabled={busy || !label.trim()}
            className="bg-accent text-bg font-medium rounded px-4 py-2 text-sm disabled:opacity-40 hover:bg-accent-dim transition-colors flex items-center gap-2"
          >
            {busy && <span className="inline-block w-3 h-3 border-2 border-bg border-t-transparent rounded-full animate-spin" />}
            {t("configuration.connect")}
          </button>
        </div>
        <p className="text-xs text-muted">
          {t("configuration.connectHint")}
        </p>
      </form>

      {error && <div className="text-danger text-sm mt-3">{error}</div>}
    </section>
  );
}

// ProjectVault is the safe deposit box: compressed (.tar.gz) archives of deleted
// projects. Each can be restored (decompressed and reactivated) or permanently
// destroyed — the latter behind a strict text confirmation (retype the project
// name), as it is the single irreversible erase of the whole lifecycle.
function ProjectVault() {
  const { t } = useTranslation();
  const [entries, setEntries] = useState<VaultEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const reload = () => api.listVault().then(setEntries).catch((e) => setError(e.message));
  useEffect(() => {
    reload();
  }, []);

  async function restore(e: VaultEntry) {
    setBusy(e.name + "restore");
    setError(null);
    try {
      await api.restoreVault(e.name);
      await reload();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setBusy(null);
    }
  }

  async function destroy(e: VaultEntry) {
    const typed = window.prompt(t("configuration.confirmDestroy", { name: e.projectName }));
    if (typed === null) return;
    if (typed.trim() !== e.projectName) {
      window.alert(t("configuration.destroyWrongName"));
      return;
    }
    setBusy(e.name + "destroy");
    setError(null);
    try {
      await api.destroyVault(e.name);
      await reload();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setBusy(null);
    }
  }

  return (
    <section className="bg-panel border border-border rounded-lg p-5 mb-6">
      <h2 className="text-sm font-medium mb-1">{t("configuration.vaultTitle")}</h2>
      <p className="text-xs text-muted mb-4">
        {t("configuration.vaultIntro")}
      </p>

      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      {entries.length === 0 ? (
        <div className="text-xs text-muted">{t("configuration.vaultEmpty")}</div>
      ) : (
        <ul className="space-y-2">
          {entries.map((e) => (
            <li
              key={e.name}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2"
            >
              <span className="font-medium text-sm truncate">{e.projectName}</span>
              <span className="text-2xs text-muted">{t("configuration.vaultDeletedAt", { date: fmtVaultDate(e.deletedAt) })}</span>
              <span className="text-2xs text-muted mono">{fmtBytes(e.size)}</span>
              {e.sessionCount > 0 && (
                <span className="text-2xs text-muted">{t("configuration.vaultSessions", { count: e.sessionCount })}</span>
              )}
              <span className="ml-auto flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => restore(e)}
                  disabled={busy === e.name + "restore"}
                  className="text-xs border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors disabled:opacity-40"
                >
                  {t("configuration.restore")}
                </button>
                <button
                  type="button"
                  onClick={() => destroy(e)}
                  disabled={busy === e.name + "destroy"}
                  className="text-xs border border-danger/50 text-danger rounded px-2 py-1 hover:bg-danger/10 transition-colors disabled:opacity-40"
                >
                  {t("configuration.destroy")}
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// deviceLabel turns a User-Agent into "Chrome · Windows"-style text; the raw
// string stays available as the row's tooltip.
function deviceLabel(ua: string): string {
  if (/okhttp|ClaudeHub/i.test(ua)) return "Claude Hub Android";
  const browser =
    /Edg\//.test(ua) ? "Edge"
    : /OPR\//.test(ua) ? "Opera"
    : /Firefox\//.test(ua) ? "Firefox"
    : /Chrome\//.test(ua) ? "Chrome"
    : /Safari\//.test(ua) ? "Safari"
    : "";
  const os =
    /Android/.test(ua) ? "Android"
    : /iPhone|iPad|iPod/.test(ua) ? "iOS"
    : /Windows/.test(ua) ? "Windows"
    : /Mac OS X/.test(ua) ? "macOS"
    : /Linux/.test(ua) ? "Linux"
    : "";
  return [browser, os].filter(Boolean).join(" · ") || ua.slice(0, 40);
}

// Devices lists every signed-in browser/app and lets the operator sign any of
// them out server-side. Hidden when the list cannot be loaded (auth disabled).
function Devices() {
  const { t } = useTranslation();
  const [devices, setDevices] = useState<Device[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const reload = () => api.listDevices().then(setDevices).catch(() => setDevices(null));
  useEffect(() => {
    reload();
  }, []);

  async function run(key: string, action: () => Promise<void>) {
    setBusy(key);
    setError(null);
    try {
      await action();
      await reload();
    } catch (err: any) {
      setError(err.message);
    } finally {
      setBusy(null);
    }
  }

  function revoke(d: Device) {
    if (d.current && !window.confirm(t("configuration.devicesConfirmCurrent"))) return;
    run(d.id, () => api.revokeDevice(d.id));
  }

  function revokeOthers() {
    if (!window.confirm(t("configuration.devicesConfirmOthers"))) return;
    run("others", () => api.revokeOtherDevices());
  }

  if (!devices) return null;

  return (
    <section className="bg-panel border border-border rounded-lg p-5 mb-6">
      <h2 className="text-sm font-medium mb-1">{t("configuration.devicesTitle")}</h2>
      <p className="text-xs text-muted mb-4">{t("configuration.devicesIntro")}</p>

      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      {devices.length === 0 ? (
        <div className="text-xs text-muted">{t("configuration.devicesEmpty")}</div>
      ) : (
        <ul className="space-y-2">
          {devices.map((d) => (
            <li
              key={d.id}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border px-3 py-2"
            >
              <span className="font-medium text-sm truncate" title={d.userAgent}>
                {deviceLabel(d.userAgent) || t("configuration.devicesUnknown")}
              </span>
              {d.current && (
                <span className="text-2xs text-ok border border-ok/40 rounded px-1.5 py-0.5">
                  {t("configuration.devicesCurrent")}
                </span>
              )}
              <span className="text-2xs text-muted">{d.email}</span>
              {d.ip && <span className="text-2xs text-muted mono">{d.ip}</span>}
              <span className="text-2xs text-muted">
                {t("configuration.devicesSignedIn", { date: fmtVaultDate(d.createdAt) })}
              </span>
              <span className="text-2xs text-muted">
                {t("configuration.devicesLastSeen", { date: fmtVaultDate(d.lastSeenAt) })}
              </span>
              <span className="text-2xs text-muted">
                {d.expiresAt
                  ? t("configuration.devicesExpires", { date: fmtVaultDate(d.expiresAt) })
                  : t("configuration.devicesNoExpiry")}
              </span>
              <span className="ml-auto">
                <button
                  type="button"
                  onClick={() => revoke(d)}
                  disabled={busy === d.id}
                  className="text-xs border border-danger/50 text-danger rounded px-2 py-1 hover:bg-danger/10 transition-colors disabled:opacity-40"
                >
                  {t("configuration.devicesRevoke")}
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}

      {devices.some((d) => !d.current) && devices.some((d) => d.current) && (
        <button
          type="button"
          onClick={revokeOthers}
          disabled={busy === "others"}
          className="mt-4 text-xs border border-border rounded px-2 py-1 hover:border-accent-dim transition-colors disabled:opacity-40"
        >
          {t("configuration.devicesRevokeOthers")}
        </button>
      )}
    </section>
  );
}

// fmtBytes renders a byte count compactly: "12.3 MB", "904 kB", "512 B".
function fmtBytes(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)} MB`;
  if (n >= 1000) return `${Math.round(n / 1000)} kB`;
  return `${n} B`;
}

// fmtVaultDate renders an ISO timestamp as a short local date.
function fmtVaultDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleString(undefined, { hourCycle: "h23" });
}

// fmtTokenExpiry renders a token expiry date compactly: "28/09/2026".
function fmtTokenExpiry(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString();
}

// ThemePicker is a client-only preference (persisted in localStorage, applied
// instantly), separate from the server-side session settings below.
function ThemePicker() {
  const { t } = useTranslation();
  const [theme, setThemeState] = useState<ThemeId>(getTheme);

  function pick(id: ThemeId) {
    setTheme(id);
    setThemeState(id);
  }

  return (
    <section className="bg-panel border border-border rounded-lg p-5 mb-6">
      <h2 className="text-sm font-medium mb-1">{t("configuration.appearanceTitle")}</h2>
      <p className="text-xs text-muted mb-4">{t("configuration.appearanceIntro")}</p>
      <div className="grid grid-cols-2 sm:grid-cols-3 gap-3">
        {THEMES.map((th) => (
          <button
            key={th.id}
            type="button"
            onClick={() => pick(th.id)}
            aria-pressed={theme === th.id}
            className={`flex items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors ${
              theme === th.id
                ? "border-accent bg-panel-2"
                : "border-border hover:border-accent-dim/50"
            }`}
          >
            <span className="flex shrink-0 overflow-hidden rounded-full border border-border">
              {th.swatch.map((c, i) => (
                <span key={i} className="h-5 w-3" style={{ background: c }} />
              ))}
            </span>
            <span className="text-sm truncate">{th.label}</span>
            {theme === th.id && <span className="ml-auto text-accent text-xs">✓</span>}
          </button>
        ))}
      </div>
    </section>
  );
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <label className="text-sm font-medium">{label}</label>
      {children}
      {hint && <span className="text-xs text-muted">{hint}</span>}
    </div>
  );
}
