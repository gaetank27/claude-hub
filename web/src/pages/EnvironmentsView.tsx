import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  api,
  type CreateContainerReq,
  type DockerContainer,
  type DockerStatus,
  type DomainStatus,
  type Project,
} from "../lib/api";

// EnvironmentsView renders the Sandbox or Live Docker environment: containers
// grouped into per-project tiles, each expandable to control the containers
// (start/stop/restart/remove) with live CPU/RAM, plus a launch form. Sandbox web
// apps get an "Ouvrir" link through the authenticated /x/ reverse proxy.
export function SandboxView() {
  return <EnvironmentView env="sandbox" />;
}
export function LiveView() {
  return <EnvironmentView env="live" />;
}

// i18n key suffixes per environment; titles/descriptions live in the
// "environments" namespace (e.g. environments.sandboxTitle).
const META = {
  sandbox: { titleKey: "sandboxTitle", descKey: "sandboxDesc" },
  live: { titleKey: "liveTitle", descKey: "liveDesc" },
} as const;

function EnvironmentView({ env }: { env: "sandbox" | "live" }) {
  const { t } = useTranslation();
  const [status, setStatus] = useState<DockerStatus | null>(null);
  const [containers, setContainers] = useState<DockerContainer[]>([]);
  const [projects, setProjects] = useState<Project[]>([]);
  const [defaultIP, setDefaultIP] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [launching, setLaunching] = useState(false);

  useEffect(() => {
    api.dockerStatus().then(setStatus).catch((e) => setError(e.message));
    api.listProjects().then(setProjects).catch(() => {});
    api.getSettings().then((s) => setDefaultIP(s.dnsServerIp || "")).catch(() => {});
  }, []);

  const available = status?.available;
  useEffect(() => {
    if (!available) return;
    const load = () =>
      api
        .listContainers(env)
        .then((c) => {
          setContainers(c);
          setError(null);
        })
        .catch((e) => setError(e.message));
    load();
    const t = setInterval(load, 4000);
    return () => clearInterval(t);
  }, [env, available]);

  const groups = useMemo(() => {
    const m = new Map<string, DockerContainer[]>();
    for (const c of containers) {
      const k = c.project || "(sans projet)";
      const arr = m.get(k);
      if (arr) arr.push(c);
      else m.set(k, [c]);
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [containers]);

  const meta = META[env];

  return (
    <div className="p-4 md:p-8 max-w-5xl mx-auto w-full">
      <div className="flex items-center justify-between gap-2 mb-1">
        <h1 className="text-2xl font-semibold">{t(`environments.${meta.titleKey}`)}</h1>
        {available && (
          <button
            onClick={() => setLaunching(true)}
            className="bg-accent text-bg font-medium rounded px-3 py-1.5 text-sm hover:bg-accent-dim transition-colors"
          >
            {t("environments.launch")}
          </button>
        )}
      </div>
      <p className="text-sm text-muted mb-5">{t(`environments.${meta.descKey}`)}</p>

      {error && <div className="text-danger text-sm mb-3">{error}</div>}

      {status && !available && (
        <div className="text-muted text-sm py-6 px-4 text-center bg-panel border border-border rounded-lg">
          {t("environments.dockerUnavailable")}{status.error ? ` : ${status.error}` : ""}.
          <div className="text-2xs text-muted/70 mt-1">
            {t("environments.dockerUnavailableHint_pre")}
            <span className="mono">deploy/docker-setup.sh</span>
            {t("environments.dockerUnavailableHint_post")}
          </div>
        </div>
      )}

      {available && groups.length === 0 && (
        <div className="text-muted text-sm py-6 text-center bg-panel border border-border rounded-lg">
          {t("environments.emptyEnv")}
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {groups.map(([project, list]) => (
          <ProjectTile
            key={project}
            project={project}
            containers={list}
            active={open === project}
            onClick={() => setOpen(open === project ? null : project)}
          />
        ))}
      </div>

      {open && groups.find(([p]) => p === open) && (
        <ContainerPanel
          project={open}
          containers={groups.find(([p]) => p === open)![1]}
          defaultIP={defaultIP}
          onError={setError}
        />
      )}

      {launching && (
        <LaunchForm
          env={env}
          projects={projects}
          onClose={() => setLaunching(false)}
          onLaunched={() => setLaunching(false)}
          onError={setError}
        />
      )}
    </div>
  );
}

function ProjectTile({
  project,
  containers,
  active,
  onClick,
}: {
  project: string;
  containers: DockerContainer[];
  active: boolean;
  onClick: () => void;
}) {
  const { t } = useTranslation();
  const running = containers.filter((c) => c.state === "running").length;
  const cpu = containers.reduce((s, c) => s + (c.cpuPercent || 0), 0);
  const mem = containers.reduce((s, c) => s + (c.memUsed || 0), 0);
  return (
    <button
      onClick={onClick}
      className={`text-left bg-panel border rounded-lg p-4 transition-colors ${
        active ? "border-accent-dim" : "border-border hover:border-accent-dim/60"
      }`}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="font-medium truncate">{project}</span>
        <span
          className={`inline-block w-2.5 h-2.5 rounded-full ${
            running > 0 ? "bg-ok" : "bg-muted/50"
          }`}
        />
      </div>
      <div className="text-xs text-muted mt-1">
        {t("environments.container", { count: containers.length })} ·{" "}
        <span className={running > 0 ? "text-ok" : ""}>
          {t("environments.activeCount", { count: running })}
        </span>
      </div>
      <div className="text-2xs text-muted/70 mt-2 flex flex-wrap gap-x-2">
        <span>{t("environments.cpu", { value: cpu.toFixed(0) })}</span>
        <span>· {t("environments.ram", { value: fmtBytes(mem) })}</span>
      </div>
    </button>
  );
}

function ContainerPanel({
  project,
  containers,
  defaultIP,
  onError,
}: {
  project: string;
  containers: DockerContainer[];
  defaultIP: string;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<string | null>(null);

  async function act(c: DockerContainer, action: "start" | "stop" | "restart" | "rm") {
    if (action === "rm" && !window.confirm(t("environments.removeConfirm", { name: c.name }))) return;
    setBusy(c.id + action);
    try {
      if (action === "rm") await api.removeContainer(c.id);
      else await api.containerAction(c.id, action);
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="bg-panel border border-border rounded-lg p-4 mt-4">
      <h2 className="font-semibold mb-3">{project}</h2>
      <div className="flex flex-col gap-2">
        {containers.map((c) => {
          const running = c.state === "running";
          const hasDomain = !!c.domainId;
          const domainActive = c.domainStatus === "active";
          const domainPending = c.domainStatus === "pending";
          const domainError = c.domainStatus === "error";
          const hasWebPort = !!c.webUrl;

          return (
            <div key={c.id} className="border border-border rounded-lg p-3 bg-bg/40">
              <div className="flex items-center justify-between gap-2">
                <span className="font-medium truncate">{c.name}</span>
                <span
                  className={`text-2xs px-1.5 py-0.5 rounded-pill ${
                    running ? "bg-ok/15 text-ok" : "bg-muted/15 text-muted"
                  }`}
                >
                  {c.status || c.state}
                </span>
              </div>
              <div className="mono text-2xs text-muted mt-1 truncate">{c.image}</div>
              <div className="text-2xs text-muted/80 mt-1 flex flex-wrap gap-x-3">
                {c.ports.filter((p) => p.public).map((p, i) => (
                  <span key={i}>
                    {p.private}→{p.public}/{p.type}
                  </span>
                ))}
                {running && <span>{t("environments.cpu", { value: c.cpuPercent.toFixed(0) })}</span>}
                {running && <span>{t("environments.ram", { value: fmtBytes(c.memUsed) })}</span>}
                {c.restartPolicy && c.restartPolicy !== "no" && (
                  <span className="text-accent">⟳ {c.restartPolicy}</span>
                )}
              </div>

              {/* URL buttons */}
              <div className="flex items-center gap-2 mt-2 flex-wrap">
                {/* Direct domain URL (custom domain) */}
                {domainActive && c.directUrl && (
                  <a
                    href={c.directUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="border border-ok/50 text-ok rounded px-2.5 py-1 text-xs hover:bg-ok/10 transition-colors"
                  >
                    {t("environments.openDirect")}
                  </a>
                )}
                {/* Internal Hub proxy URL */}
                {c.webUrl && running && (
                  <a
                    href={c.webUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="border border-accent-dim text-accent rounded px-2.5 py-1 text-xs hover:bg-accent/10 transition-colors"
                  >
                    {domainActive ? t("environments.openHub") : t("environments.openLink")}
                  </a>
                )}
                {/* Domain status badges */}
                {domainPending && (
                  <span className="text-2xs text-muted/70 animate-pulse">
                    {t("environments.domainPending")}
                  </span>
                )}
                {domainError && (
                  <span className="text-2xs text-danger">{t("environments.domainError")}</span>
                )}
              </div>

              {/* Lifecycle buttons */}
              <div className="flex items-center gap-2 mt-2 flex-wrap">
                {running ? (
                  <ActBtn label={t("environments.stop")} onClick={() => act(c, "stop")} busy={busy === c.id + "stop"} />
                ) : (
                  <ActBtn label={t("environments.start")} onClick={() => act(c, "start")} busy={busy === c.id + "start"} />
                )}
                <ActBtn label={t("environments.restart")} onClick={() => act(c, "restart")} busy={busy === c.id + "restart"} />
                <button
                  onClick={() => act(c, "rm")}
                  disabled={busy === c.id + "rm"}
                  className="text-danger rounded px-2 py-1 text-xs hover:underline disabled:opacity-40 ml-auto"
                >
                  {t("common.delete")}
                </button>
              </div>

              {/* Domain management */}
              {hasWebPort && (
                <ContainerDomainSection
                  container={c}
                  hasDomain={hasDomain}
                  defaultIP={defaultIP}
                  onError={onError}
                />
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ContainerDomainSection manages the custom domain for a single container.
function ContainerDomainSection({
  container,
  hasDomain,
  defaultIP,
  onError,
}: {
  container: DockerContainer;
  hasDomain: boolean;
  defaultIP: string;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [domain, setDomain] = useState("");
  const [serverIP, setServerIP] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState<DomainStatus | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Poll domain status while provisioning (domainId exists but not done).
  useEffect(() => {
    if (!container.domainId || container.domainStatus !== "pending") return;
    const id = container.domainId;
    const poll = () =>
      api.getDomainStatus(id).then(setStatus).catch(() => {});
    poll();
    pollRef.current = setInterval(poll, 3000);
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [container.domainId, container.domainStatus]);

  // Stop polling when done.
  useEffect(() => {
    if (status?.done && pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  }, [status]);

  function openForm(prefillDomain = "", prefillIP = "") {
    setDomain(prefillDomain);
    setServerIP(prefillIP || defaultIP);
    setOpen(true);
  }

  function closeForm() {
    setOpen(false);
    setDomain("");
    setServerIP("");
  }

  async function provision() {
    if (!domain.trim()) return;
    setBusy(true);
    try {
      if (hasDomain) await api.removeContainerDomain(container.name);
      await api.provisionContainerDomain(container.name, domain.trim(), serverIP.trim() || undefined);
      closeForm();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  }

  async function removeDomain() {
    if (!window.confirm(t("environments.domainRemoveConfirm", { domain: container.directUrl || container.domainId }))) return;
    setBusy(true);
    try {
      await api.removeContainerDomain(container.name);
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  }

  const form = (
    <div className="flex flex-col gap-1.5 mt-1">
      <div className="flex items-center gap-2">
        <input
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && provision()}
          placeholder={t("environments.domainFieldPlaceholder")}
          className="flex-1 bg-bg border border-border rounded px-2 py-1 text-xs focus:border-accent-dim outline-none"
          autoFocus
        />
        <input
          value={serverIP}
          onChange={(e) => setServerIP(e.target.value)}
          placeholder={t("environments.domainIPPlaceholder")}
          className="w-36 bg-bg border border-border rounded px-2 py-1 text-xs focus:border-accent-dim outline-none font-mono"
        />
        <button
          onClick={provision}
          disabled={busy || !domain.trim()}
          className="bg-accent text-bg rounded px-2.5 py-1 text-xs font-medium hover:bg-accent-dim disabled:opacity-40 transition-colors"
        >
          {busy ? t("environments.domainProvisioning") : t("environments.domainProvision")}
        </button>
        <button onClick={closeForm} className="text-muted text-xs hover:text-fg">✕</button>
      </div>
    </div>
  );

  if (hasDomain) {
    return (
      <div className="mt-2 pt-2 border-t border-border/50">
        {/* Show provisioning logs while pending */}
        {status && !status.done && status.logs.length > 0 && (
          <div className="text-2xs text-muted/70 font-mono mb-1.5 max-h-16 overflow-y-auto">
            {status.logs.map((l, i) => (
              <div key={i} className={l.startsWith("ERROR") ? "text-danger" : ""}>{l}</div>
            ))}
          </div>
        )}
        {open ? form : (
          <div className="flex items-center gap-3">
            <span className="text-2xs text-muted font-mono truncate">{container.directUrl || container.domainId}</span>
            <button
              onClick={() => openForm(container.directUrl ?? "", "")}
              disabled={busy}
              className="text-2xs text-muted hover:text-accent transition-colors disabled:opacity-40"
            >
              {t("environments.domainEdit")}
            </button>
            <button
              onClick={removeDomain}
              disabled={busy}
              className="text-2xs text-muted hover:text-danger transition-colors disabled:opacity-40"
            >
              {t("environments.domainRemove")}
            </button>
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="mt-2 pt-2 border-t border-border/50">
      {!open ? (
        <button
          onClick={() => openForm()}
          className="text-2xs text-muted hover:text-accent transition-colors"
        >
          {t("environments.addDomain")}
        </button>
      ) : form}
    </div>
  );
}

function ActBtn({ label, onClick, busy }: { label: string; onClick: () => void; busy: boolean }) {
  return (
    <button
      onClick={onClick}
      disabled={busy}
      className="border border-border text-fg rounded px-2.5 py-1 text-xs hover:border-accent-dim disabled:opacity-40 transition-colors"
    >
      {busy ? "…" : label}
    </button>
  );
}

function LaunchForm({
  env,
  projects,
  onClose,
  onLaunched,
  onError,
}: {
  env: "sandbox" | "live";
  projects: Project[];
  onClose: () => void;
  onLaunched: () => void;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const [project, setProject] = useState(projects[0]?.slug ?? "");
  const [image, setImage] = useState("");
  const [name, setName] = useState("");
  const [ports, setPorts] = useState("");
  const [webPort, setWebPort] = useState("");
  const [envVars, setEnvVars] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!project || !image.trim()) {
      onError(t("environments.requiredFields"));
      return;
    }
    const portList = ports
      .split(",")
      .map((p) => parseInt(p.trim(), 10))
      .filter((n) => !isNaN(n));
    const vars: Record<string, string> = {};
    for (const line of envVars.split("\n")) {
      const i = line.indexOf("=");
      if (i > 0) vars[line.slice(0, i).trim()] = line.slice(i + 1).trim();
    }
    const body: CreateContainerReq = {
      project,
      env,
      image: image.trim(),
      name: name.trim() || undefined,
      ports: portList,
      envVars: vars,
      webPort: webPort ? parseInt(webPort, 10) : undefined,
    };
    setBusy(true);
    try {
      await api.createContainer(body);
      onLaunched();
    } catch (e: any) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="bg-panel border border-border rounded-lg p-4 mt-4">
      <div className="flex items-center justify-between mb-3">
        <h2 className="font-semibold">{t("environments.launchTitle", { env })}</h2>
        <button onClick={onClose} className="text-muted hover:text-fg text-sm">
          {t("common.close")}
        </button>
      </div>
      <div className="flex flex-col gap-3">
        <Field label={t("environments.fieldProject")}>
          <select
            value={project}
            onChange={(e) => setProject(e.target.value)}
            className="w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
          >
            {projects.map((p) => (
              <option key={p.id} value={p.slug}>
                {p.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label={t("environments.fieldImage")}>
          <Inp value={image} onChange={setImage} placeholder={t("environments.imagePlaceholder")} />
        </Field>
        <Field label={t("environments.fieldName")}>
          <Inp value={name} onChange={setName} placeholder={t("environments.namePlaceholder")} />
        </Field>
        <Field label={t("environments.fieldPorts")}>
          <Inp value={ports} onChange={setPorts} placeholder={t("environments.portsPlaceholder")} />
        </Field>
        <Field label={t("environments.fieldWebPort")}>
          <Inp value={webPort} onChange={setWebPort} placeholder={t("environments.webPortPlaceholder")} />
        </Field>
        <Field label={t("environments.fieldEnvVars")}>
          <textarea
            value={envVars}
            onChange={(e) => setEnvVars(e.target.value)}
            rows={3}
            className="w-full bg-bg border border-border rounded px-3 py-2 text-sm font-mono focus:border-accent-dim outline-none"
            placeholder={t("environments.envVarsPlaceholder")}
          />
        </Field>
        {env === "live" && (
          <div className="text-2xs text-muted/70">
            {t("environments.liveRestartHint_pre")}
            <span className="mono">unless-stopped</span>
            {t("environments.liveRestartHint_post")}
          </div>
        )}
        <button
          onClick={submit}
          disabled={busy}
          className="self-start bg-accent text-bg font-medium rounded px-4 py-1.5 text-sm hover:bg-accent-dim disabled:opacity-40 transition-colors"
        >
          {busy ? t("environments.launchingPull") : t("environments.submitLaunch")}
        </button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs text-muted">{label}</span>
      {children}
    </label>
  );
}

function Inp({
  value,
  onChange,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  return (
    <input
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      className="w-full bg-bg border border-border rounded px-3 py-2 text-sm focus:border-accent-dim outline-none"
    />
  );
}

// Bytes → human size (binary units).
function fmtBytes(n: number): string {
  if (!n) return "0";
  const u = ["o", "Ki", "Mi", "Gi", "Ti"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i >= 3 ? 1 : 0)} ${u[i]}`;
}
