import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";

const inputCls = "bg-bg border border-border rounded px-2 py-1 text-sm focus:border-accent-dim outline-none";
const btnCls = "text-xs px-2 py-1 rounded border border-border hover:border-accent-dim disabled:opacity-50";

// DnsProviderPanel chooses, per zone, which DNS host the Hub writes to and uses for
// certificate (DNS-01) challenges. It never touches name servers: switch a zone only
// once it is really served by the new provider. The dry-run sends nothing anywhere.
export function DnsProviderPanel() {
  const { t } = useTranslation();
  const [st, setSt] = useState<Awaited<ReturnType<typeof api.dnsProviders>> | null>(null);
  const [zone, setZone] = useState("");
  const [provider, setProvider] = useState("infomaniak");
  const [op, setOp] = useState("create");
  const [rec, setRec] = useState({ type: "A", name: "", content: "" });
  const [plan, setPlan] = useState<Awaited<ReturnType<typeof api.dnsDryRun>> | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => { api.dnsProviders().then(setSt).catch(() => setSt(null)); }, []);
  useEffect(load, [load]);
  if (!st) return null;

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true); setMsg(null);
    try { await fn(); } catch (e: any) { setMsg({ ok: false, text: String(e?.message ?? e) }); }
    setBusy(false);
  };
  const zones = Object.entries(st.zones);

  return (
    <div className="border border-border rounded p-3 mb-3 text-xs flex flex-col gap-2">
      <div className="font-medium text-sm">{t("tools.dns.title")}</div>
      <p className="text-muted">{t("tools.dns.intro", { def: st.default })}</p>
      <ul className="list-disc ml-4">
        {zones.length === 0 && <li className="text-muted">{t("tools.dns.none", { def: st.default })}</li>}
        {zones.map(([z, p]) => <li key={z}><span className="font-mono">{z}</span> → <b>{p}</b></li>)}
      </ul>
      <div className="flex flex-wrap gap-2 items-center">
        <input className={inputCls} placeholder="example.com" value={zone} onChange={(e) => setZone(e.target.value.trim())} />
        <select className={inputCls} value={provider} onChange={(e) => setProvider(e.target.value)}>
          {st.providers.map((p) => <option key={p} value={p} disabled={!st.configured[p] && p !== st.default}>{p}{st.configured[p] ? "" : ` (${t("tools.dns.noConnector")})`}</option>)}
        </select>
        <button className={btnCls} disabled={busy || !zone} onClick={() => {
          if (!window.confirm(t("tools.dns.confirm", { zone, provider }))) return;
          run(async () => { setSt(await api.dnsSetProvider(zone, provider)); setMsg({ ok: true, text: t("tools.dns.saved") }); });
        }}>{t("tools.dns.apply")}</button>
      </div>
      <div className="text-muted">{t("tools.dns.dryTitle")}</div>
      <div className="flex flex-wrap gap-2 items-center">
        <select className={inputCls} value={op} onChange={(e) => setOp(e.target.value)}>
          <option value="create">create</option><option value="delete">delete</option><option value="cert">cert</option>
        </select>
        {op !== "cert" && <>
          <input className={inputCls + " w-16"} value={rec.type} onChange={(e) => setRec({ ...rec, type: e.target.value })} />
          <input className={inputCls} placeholder="hub.example.com" value={rec.name} onChange={(e) => setRec({ ...rec, name: e.target.value })} />
          <input className={inputCls} placeholder="1.2.3.4" value={rec.content} onChange={(e) => setRec({ ...rec, content: e.target.value })} />
        </>}
        <button className={btnCls} disabled={busy || !zone} onClick={() => run(async () => setPlan(await api.dnsDryRun({ zone, op, ...rec })))}>{t("tools.dns.dry")}</button>
        {busy && <span className="animate-pulse text-muted">…</span>}
      </div>
      {plan && (
        <div className="border border-border rounded p-2">
          <div>{plan.zone} · <b>{plan.provider}</b> · {plan.configured ? t("tools.dns.connectorOk") : t("tools.dns.connectorKo")}</div>
          {plan.steps.map((s, i) => <div key={i} className="font-mono break-all">{i + 1}. {s.do} — <span className="text-muted">{s.detail}</span></div>)}
          {plan.warnings.map((w, i) => <div key={i} className="text-danger">⚠ {w}</div>)}
          <div className="text-muted">{t("tools.dns.nothingSent")}</div>
        </div>
      )}
      {msg && <div className={msg.ok ? "text-accent" : "text-danger"}>{msg.text}</div>}
    </div>
  );
}
