import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../lib/api";

type Tab = "zones" | "dns" | "tunnels" | "access" | "journal";

const inputCls = "bg-bg border border-border rounded px-2 py-1 text-sm focus:border-accent-dim outline-none";
const btnCls = "text-xs px-2 py-1 rounded border border-border hover:border-accent-dim disabled:opacity-50";

// CloudflarePanel is the management view of the Cloudflare connector: zones, DNS
// records, tunnels and Access applications. Everything shown comes from the
// Cloudflare API (untrusted data, rendered as text only). Every change asks for a
// confirmation naming the exact change, and the backend refuses it unless the
// connector's « Allow changes » switch is on; each one is journaled.
export function CloudflarePanel({ onChanged }: { onChanged: () => Promise<void> | void }) {
  const { t } = useTranslation();
  const [st, setSt] = useState<{ configured: boolean; allowWrites: boolean } | null>(null);
  const [tab, setTab] = useState<Tab>("zones");
  const [rows, setRows] = useState<any[]>([]);
  const [zone, setZone] = useState("");
  const [zones, setZones] = useState<any[]>([]);
  const [loading, setLoading] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [form, setForm] = useState<Record<string, string>>({});
  const [reveal, setReveal] = useState<{ service: string; docker: string } | null>(null);
  const f = (k: string) => form[k] ?? "";
  const setF = (k: string, v: string) => setForm((o) => ({ ...o, [k]: v }));

  useEffect(() => {
    api.cfStatus().then(setSt).catch(() => setSt({ configured: false, allowWrites: false }));
    api.cfRead("zones").then((z) => { setZones(z); if (z[0]) setZone((c) => c || z[0].id); }).catch(() => {});
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setMsg(null);
    try {
      if (tab === "zones") setRows(zones.length ? zones : await api.cfRead("zones"));
      else if (tab === "dns") setRows(zone ? await api.cfRead("dns", { zone }) : []);
      else if (tab === "tunnels") setRows(await api.cfRead("tunnels"));
      else if (tab === "access") setRows(await api.cfRead("apps"));
      else setRows(await api.cfAudit());
    } catch (e: any) {
      setRows([]);
      setMsg({ ok: false, text: e.message });
    } finally {
      setLoading(false);
    }
  }, [tab, zone, zones]);
  useEffect(() => { if (st?.configured) load(); }, [load, st]);

  async function act(action: string, args: Record<string, unknown>, what: string) {
    if (!window.confirm(t("tools.cf.confirmAct", { what }))) return;
    setMsg(null);
    try {
      const r = await api.cfAct(action, args);
      setMsg({ ok: true, text: r.result });
      await load();
    } catch (e: any) {
      setMsg({ ok: false, text: e.message });
    }
  }

  async function showToken(id: string) {
    if (!window.confirm(t("tools.cf.tokenWarn"))) return;
    try {
      const r = await api.cfTunnelToken(id);
      setReveal({ service: r.service, docker: r.docker });
    } catch (e: any) {
      setMsg({ ok: false, text: e.message });
    }
  }

  if (!st) return null;
  if (!st.configured) return <p className="text-sm text-muted mb-3">{t("tools.cf.notConfigured")}</p>;
  const w = st.allowWrites;
  const del = (action: string, args: Record<string, unknown>, what: string) =>
    w && <button className={btnCls + " text-danger"} onClick={() => act(action, args, what)}>{t("tools.cf.delete")}</button>;

  return (
    <div className="border border-border rounded p-3 mb-4">
      <p className="text-xs text-muted mb-2">{t("tools.cf.intro")}</p>
      {!w && <p className="text-xs text-danger mb-2">{t("tools.cf.writesOff")}</p>}
      <div className="flex gap-2 mb-3 flex-wrap items-center">
        {(["zones", "dns", "tunnels", "access", "journal"] as Tab[]).map((k) => (
          <button key={k} onClick={() => { setTab(k); setReveal(null); }}
            className={btnCls + (tab === k ? " border-accent-dim text-accent" : "")}>{t(`tools.cf.${k}`)}</button>
        ))}
        <button className={btnCls} onClick={load}>{t("tools.cf.refresh")}</button>
        {tab === "dns" && (
          <select value={zone} onChange={(e) => setZone(e.target.value)} className={inputCls}>
            {zones.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}
          </select>
        )}
      </div>
      {msg && <div className={"text-xs mb-2 whitespace-pre-wrap " + (msg.ok ? "text-ok" : "text-danger")}>{msg.text}</div>}
      {loading && <div className="text-xs text-muted animate-pulse">{t("tools.cf.loading")}</div>}
      {!loading && rows.length === 0 && <div className="text-xs text-muted">{t("tools.cf.none")}</div>}

      <ul className="text-xs flex flex-col gap-1">
        {tab === "zones" && rows.map((z) => (
          <li key={z.id} className="flex gap-3"><span className="font-medium">{z.name}</span><span className="text-muted">{z.status}</span><span className="text-muted">{(z.name_servers ?? []).join(", ")}</span></li>
        ))}
        {tab === "dns" && rows.map((r) => (
          <li key={r.id} className="flex gap-2 items-center flex-wrap">
            <span className="w-12 text-muted">{r.type}</span><span className="font-medium">{r.name}</span>
            <span className="text-muted break-all">→ {r.content}</span>{r.proxied && <span className="text-accent">☁ {t("tools.cf.proxied")}</span>}
            {del("cloudflare_delete_dns_record", { zone_id: zone, record_id: r.id }, `DELETE ${r.type} ${r.name} → ${r.content}`)}
          </li>
        ))}
        {tab === "tunnels" && rows.map((r) => (
          <li key={r.id} className="flex flex-col gap-1">
            <div className="flex gap-2 items-center flex-wrap">
              <span className="font-medium">{r.name}</span><span className={r.status === "healthy" ? "text-ok" : "text-muted"}>{r.status}</span>
              {w && <button className={btnCls} onClick={() => showToken(r.id)}>{t("tools.cf.showToken")}</button>}
              {w && <button className={btnCls} onClick={() => setForm((o) => ({ ...o, routeTunnel: r.id }))}>{t("tools.cf.edit")}</button>}
              {del("cloudflare_delete_tunnel", { tunnel_id: r.id }, `DELETE tunnel ${r.name}`)}
            </div>
            {f("routeTunnel") === r.id && (
              <div className="flex flex-col gap-1 ml-2">
                <textarea rows={3} className={inputCls} value={f("routes")} onChange={(e) => setF("routes", e.target.value)} placeholder={t("tools.cf.routes")} />
                <p className="text-danger">{t("tools.cf.routesWarn")}</p>
                <div className="flex gap-2 items-center">
                  <select className={inputCls} value={f("routeZone") || zone} onChange={(e) => setF("routeZone", e.target.value)}>
                    {zones.map((z) => <option key={z.id} value={z.id}>{z.name}</option>)}
                  </select>
                  <button className={btnCls} onClick={() => {
                    const routes = f("routes").split("\n").map((x) => x.trim()).filter(Boolean);
                    act("cloudflare_set_tunnel_routes", { tunnel_id: r.id, routes, zone_id: f("routeZone") || zone }, routes.join("\n"));
                  }}>{t("tools.cf.saveRoutes")}</button>
                </div>
              </div>
            )}
          </li>
        ))}
        {tab === "access" && rows.map((a) => (
          <li key={a.id} className="flex gap-2 items-center flex-wrap">
            <span className="font-medium">{a.name}</span><span className="text-muted">{a.domain}</span>
            <span className="text-muted">{a.session_duration}</span>
            <span className="text-muted">{(a.policies ?? []).map((p: any) => `${p.name} (${p.decision})`).join(", ")}</span>
            {del("cloudflare_delete_access_app", { app_id: a.id }, `DELETE Access app ${a.name} (${a.domain})`)}
          </li>
        ))}
        {tab === "journal" && rows.map((a) => (
          <li key={a.id} className="flex gap-2 flex-wrap">
            <span className="text-muted">{new Date(a.ts).toLocaleString()}</span>
            <span className={a.outcome === "ok" ? "text-ok" : "text-danger"}>{a.outcome}</span>
            <span>{a.action}</span><span className="text-muted">[{a.source}]</span>
            <span className="text-muted break-all">{a.detail}</span>
          </li>
        ))}
      </ul>

      {reveal && (
        <div className="mt-3 text-xs flex flex-col gap-1">
          <div className="text-muted">{t("tools.cf.installService")}</div>
          <code className="bg-bg border border-border rounded p-2 break-all select-all">{reveal.service}</code>
          <div className="text-muted">{t("tools.cf.installDocker")}</div>
          <code className="bg-bg border border-border rounded p-2 break-all select-all">{reveal.docker}</code>
          <button className={btnCls + " self-start"} onClick={() => setReveal(null)}>{t("common.close")}</button>
        </div>
      )}

      {w && tab === "dns" && zone && (
        <div className="mt-3 flex gap-2 flex-wrap items-center text-xs">
          <select className={inputCls} value={f("type") || "CNAME"} onChange={(e) => setF("type", e.target.value)}>
            {["A", "AAAA", "CNAME", "TXT", "MX"].map((x) => <option key={x}>{x}</option>)}
          </select>
          <input className={inputCls} placeholder="name" value={f("name")} onChange={(e) => setF("name", e.target.value)} />
          <input className={inputCls} placeholder="content" value={f("content")} onChange={(e) => setF("content", e.target.value)} />
          <label className="flex gap-1 items-center"><input type="checkbox" checked={f("proxied") === "1"} onChange={(e) => setF("proxied", e.target.checked ? "1" : "")} />{t("tools.cf.proxied")}</label>
          <button className={btnCls} onClick={() => act("cloudflare_create_dns_record",
            { zone_id: zone, type: f("type") || "CNAME", name: f("name"), content: f("content"), proxied: f("proxied") === "1" },
            `${f("type") || "CNAME"} ${f("name")} → ${f("content")}${f("proxied") === "1" ? " (proxied)" : ""}`)}>{t("tools.cf.add")}</button>
        </div>
      )}
      {w && tab === "tunnels" && (
        <div className="mt-3 flex gap-2 items-center text-xs">
          <input className={inputCls} placeholder={t("tools.cf.tunnelName")} value={f("tunnel")} onChange={(e) => setF("tunnel", e.target.value)} />
          <button className={btnCls} onClick={() => act("cloudflare_create_tunnel", { name: f("tunnel") }, `Create tunnel ${f("tunnel")}`)}>{t("tools.cf.createTunnel")}</button>
        </div>
      )}
      {w && tab === "access" && (
        <div className="mt-3 flex flex-col gap-1 text-xs">
          <p className="text-muted">{t("tools.cf.openWarn")}</p>
          <div className="flex gap-2 flex-wrap">
            <input className={inputCls} placeholder={t("tools.cf.appName")} value={f("appName")} onChange={(e) => setF("appName", e.target.value)} />
            <input className={inputCls} placeholder={t("tools.cf.appDomain")} value={f("appDomain")} onChange={(e) => setF("appDomain", e.target.value)} />
            <input className={inputCls + " w-72"} placeholder={t("tools.cf.appEmails")} value={f("appEmails")} onChange={(e) => setF("appEmails", e.target.value)} />
            <input className={inputCls + " w-28"} placeholder={t("tools.cf.appSession")} value={f("appSession")} onChange={(e) => setF("appSession", e.target.value)} />
            <button className={btnCls} onClick={() => {
              const emails = f("appEmails").split(",").map((x) => x.trim()).filter(Boolean);
              act("cloudflare_create_access_app", { name: f("appName"), domain: f("appDomain"), emails, session_duration: f("appSession") || undefined },
                `Access ${f("appName")} on ${f("appDomain")}: only ${emails.join(", ")}`);
            }}>{t("tools.cf.createApp")}</button>
          </div>
        </div>
      )}

      <div className="mt-4 pt-3 border-t border-border">
        <button className={btnCls + " text-danger"} onClick={async () => {
          if (!window.confirm(t("tools.cf.revokeConfirm"))) return;
          try { const r = await api.cfRevoke(); setMsg({ ok: true, text: r.note }); await onChanged(); setSt({ configured: false, allowWrites: false }); }
          catch (e: any) { setMsg({ ok: false, text: e.message }); }
        }}>{t("tools.cf.revoke")}</button>
      </div>
    </div>
  );
}
