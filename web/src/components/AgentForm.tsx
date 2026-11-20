import { useState, type FormEvent, type ReactNode } from "react";
import { Bot, Cpu, Eye, EyeOff, KeyRound, Link2, Terminal } from "lucide-react";
import {
  api,
  ApiError,
  nameInput,
  type ACPProvider,
  type Computer,
  type DriverConfigInput,
  type DriverKind,
  type Member,
} from "../api";
import { PixelAvatar } from "./Avatar";
import { Button, Field, Select, TextArea } from "./ui";

const PROVIDERS: { id: ACPProvider; name: string; desc: string }[] = [
  { id: "pi", name: "pi", desc: "Pi coding agent, uses your pi login" },
  { id: "codex", name: "Codex", desc: "OpenAI Codex CLI via ACP" },
  { id: "claude", name: "Claude Code", desc: "Claude Code via ACP" },
];

export const ROLE_PRESETS = [
  { label: "Engineer", text: "Senior software engineer. Reads the codebase, proposes small, reviewed changes, and reports results concisely." },
  { label: "Researcher", text: "Research assistant. Gathers sources, compares options, and writes short summaries with links." },
  { label: "Coordinator", text: "Project coordinator. Breaks requests into Tasks, assigns owners, and keeps status up to date." },
];

export interface DriverDraft {
  kind: DriverKind;
  provider: ACPProvider;
  command: string;
  piProvider: string;
  piModel: string;
  piPackages: string;
  baseURL: string;
  model: string;
  apiKey: string;
}

export function splitCommandArgs(raw: string): { args: string[]; error?: string } {
  const args: string[] = [];
  let cur = "";
  let quote: "'" | '"' | null = null;
  let inToken = false;
  for (let i = 0; i < raw.length; i++) {
    const ch = raw[i];
    if (quote) {
      if (ch === "\\" && quote === '"' && i + 1 < raw.length) {
        cur += raw[++i];
      } else if (ch === quote) {
        quote = null;
      } else {
        cur += ch;
      }
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      inToken = true;
    } else if (/\s/.test(ch)) {
      if (inToken) {
        args.push(cur);
        cur = "";
        inToken = false;
      }
    } else {
      cur += ch;
      inToken = true;
    }
  }
  if (quote) return { args: [], error: "Unclosed quote in command" };
  if (inToken) args.push(cur);
  return { args };
}

export function draftFromAgent(m?: Member): DriverDraft {
  const cfg = m?.agent?.driver_config;
  return {
    kind: m?.agent?.driver_kind ?? "acp",
    provider: cfg?.provider ?? "pi",
    command: cfg?.command?.map((a) => (/\s/.test(a) ? JSON.stringify(a) : a)).join(" ") ?? "",
    piProvider: cfg?.pi_provider ?? "",
    piModel: cfg?.pi_model ?? "",
    piPackages: cfg?.pi_packages?.join(", ") ?? "",
    baseURL: cfg?.base_url ?? "",
    model: cfg?.model ?? "",
    apiKey: "",
  };
}

export function validateDriver(d: DriverDraft): Partial<Record<"baseURL" | "model" | "provider" | "command", string>> {
  if (d.kind === "acp") {
    const parsed = splitCommandArgs(d.command);
    return parsed.error ? { command: parsed.error } : {};
  }
  const errs: Partial<Record<"baseURL" | "model", string>> = {};
  if (!/^https?:\/\/[^\s]+$/i.test(d.baseURL.trim())) errs.baseURL = "Enter an http(s) URL, e.g. http://127.0.0.1:4000/v1";
  if (!d.model.trim()) errs.model = "Model is required";
  return errs;
}

export function driverInput(d: DriverDraft): DriverConfigInput {
  if (d.kind === "acp") {
    const { args } = splitCommandArgs(d.command);
    const cfg: { provider?: ACPProvider; command?: string[]; pi_provider?: string; pi_model?: string; pi_packages?: string[] } = {
      provider: d.provider,
    };
    if (args.length > 0) cfg.command = args;
    if (d.provider === "pi") {
      const packages = d.piPackages.split(",").map((p) => p.trim()).filter(Boolean);
      if (d.piProvider.trim()) cfg.pi_provider = d.piProvider.trim();
      if (d.piModel.trim()) cfg.pi_model = d.piModel.trim();
      if (packages.length > 0) cfg.pi_packages = packages;
    }
    return cfg;
  }
  const cfg: { base_url: string; model: string; api_key?: string } = { base_url: d.baseURL.trim(), model: d.model.trim() };
  if (d.apiKey) cfg.api_key = d.apiKey;
  return cfg;
}

function KindCard({
  active,
  onClick,
  icon,
  title,
  body,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  title: string;
  body: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={`flex-1 text-left p-3.5 rounded-[12px] border transition-[border-color,background-color,box-shadow] duration-150 ${
        active ? "border-accent bg-accent-soft shadow-[var(--ring)]" : "border-line-strong bg-card hover:border-fg-4"
      }`}
    >
      <div className="flex items-center gap-2">
        <span className={`size-7 rounded-[8px] flex items-center justify-center [&>svg]:size-4 ${active ? "bg-accent text-accent-fg" : "bg-subtle text-fg-2"}`}>
          {icon}
        </span>
        <span className="text-[14px] font-semibold text-fg">{title}</span>
      </div>
      <p className="mt-2 text-[12.5px] text-fg-3 leading-snug">{body}</p>
    </button>
  );
}

/** Driver section shared by agent creation and the agent detail editor. */
export function DriverFields({
  draft,
  onChange,
  computer,
  errors = {},
  apiKeySet,
}: {
  draft: DriverDraft;
  onChange: (d: DriverDraft) => void;
  computer?: Computer;
  errors?: Partial<Record<"baseURL" | "model" | "provider" | "command", string>>;
  apiKeySet?: boolean;
}) {
  const [showKey, setShowKey] = useState(false);
  const available = computer?.acp_providers;
  const hasCustomCommand = draft.command.trim().length > 0;
  const set = (p: Partial<DriverDraft>) => onChange({ ...draft, ...p });

  return (
    <div className="space-y-4">
      <div className="flex gap-2.5">
        <KindCard
          active={draft.kind === "acp"}
          onClick={() => set({ kind: "acp" })}
          icon={<Terminal />}
          title="Coding agent"
          body="Run pi, Codex or Claude Code on the computer through ACP."
        />
        <KindCard
          active={draft.kind === "builtin"}
          onClick={() => set({ kind: "builtin" })}
          icon={<Cpu />}
          title="Model API"
          body="Sumi's built-in harness with any OpenAI-compatible endpoint."
        />
      </div>

      {draft.kind === "acp" ? (
        <div className="space-y-3.5 animate-fade-in">
          <div className="space-y-2" role="radiogroup" aria-label="ACP provider">
            {PROVIDERS.map((p) => {
              const ok = hasCustomCommand || !available || available.includes(p.id);
              const active = draft.provider === p.id;
              return (
                <button
                  key={p.id}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  disabled={!ok}
                  onClick={() => set({ provider: p.id })}
                  className={`w-full px-3.5 py-2.5 rounded-[10px] border flex items-center gap-3 text-left transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
                    active ? "border-accent bg-accent-soft" : "border-line-strong bg-card hover:border-fg-4"
                  }`}
                >
                  <span className={`size-4 rounded-full border-2 shrink-0 flex items-center justify-center ${active ? "border-accent" : "border-fg-4"}`}>
                    {active && <span className="size-2 rounded-full bg-accent" />}
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="block text-[14px] font-medium text-fg">{p.name}</span>
                    <span className="block text-[12.5px] text-fg-3">{ok ? p.desc : `Not found on ${computer?.name ?? "this computer"}`}</span>
                  </span>
                </button>
              );
            })}
          </div>
          <Field
            label="Custom command"
            optional
            leading={<Terminal />}
            value={draft.command}
            onChange={(e) => set({ command: e.target.value })}
            placeholder="ak acp claude"
            hint="Overrides the default ACP launcher (e.g. ak acp claude). Leave empty for the provider default."
            error={errors.command}
            autoComplete="off"
            spellCheck={false}
          />
          {draft.provider === "pi" ? (
            <div className="space-y-3.5 rounded-[10px] border border-line-strong bg-card p-3.5">
              <p className="text-[12.5px] text-fg-3 leading-snug">
                Isolate this agent onto a specific pi provider. Leave empty to follow the host pi settings.
              </p>
              <Field
                label="pi provider"
                optional
                leading={<Bot />}
                value={draft.piProvider}
                onChange={(e) => set({ piProvider: e.target.value })}
                placeholder="ak-opendesign-pi"
                hint="Written as defaultProvider in this agent's isolated pi settings."
                autoComplete="off"
                spellCheck={false}
              />
              <Field
                label="pi model"
                optional
                leading={<Cpu />}
                value={draft.piModel}
                onChange={(e) => set({ piModel: e.target.value })}
                placeholder="deepseek-v4.1-flash"
                hint="Written as defaultModel."
                autoComplete="off"
                spellCheck={false}
              />
              <Field
                label="pi packages"
                optional
                leading={<Terminal />}
                value={draft.piPackages}
                onChange={(e) => set({ piPackages: e.target.value })}
                placeholder="npm:pi-commandcode-provider"
                hint="Comma-separated npm: specs. Only these provider plugins load in the isolated dir; host npm is linked from the host pi dir."
                autoComplete="off"
                spellCheck={false}
              />
            </div>
          ) : null}
        </div>
      ) : (
        <div className="space-y-3.5 animate-fade-in">
          <Field
            label="Base URL"
            leading={<Link2 />}
            value={draft.baseURL}
            onChange={(e) => set({ baseURL: e.target.value })}
            placeholder="http://127.0.0.1:4000/v1"
            error={errors.baseURL}
            autoComplete="off"
            spellCheck={false}
          />
          <Field
            label="Model"
            leading={<Bot />}
            value={draft.model}
            onChange={(e) => set({ model: e.target.value })}
            placeholder="gpt-5-mini"
            error={errors.model}
            autoComplete="off"
            spellCheck={false}
          />
          <Field
            label="API key"
            optional
            leading={<KeyRound />}
            type={showKey ? "text" : "password"}
            value={draft.apiKey}
            onChange={(e) => set({ apiKey: e.target.value })}
            placeholder={apiKeySet ? "•••••••• saved — leave empty to keep" : "sk-…"}
            hint="Stored on the server and sent only to the computer that runs this agent. Never shown again."
            autoComplete="off"
            spellCheck={false}
            trailing={
              <button
                type="button"
                onClick={() => setShowKey((v) => !v)}
                aria-label={showKey ? "Hide API key" : "Show API key"}
                className="size-7 rounded-[6px] text-fg-3 hover:text-fg hover:bg-hover flex items-center justify-center"
              >
                {showKey ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
              </button>
            }
          />
        </div>
      )}
    </div>
  );
}

function fieldFromError(err: unknown): string {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : "Could not create agent";
}

export function AgentForm({
  spaceSlug,
  computers,
  onCreated,
  footer,
}: {
  spaceSlug: string;
  computers: Computer[];
  onCreated: (agent: Member) => void;
  footer?: (submit: ReactNode) => ReactNode;
}) {
  const [name, setName] = useState("Lin");
  const [role, setRole] = useState(ROLE_PRESETS[0].text);
  const [computerID, setComputerID] = useState(
    () => computers.find((c) => c.connection_status === "online")?.id ?? computers[0]?.id ?? "",
  );
  const [draft, setDraft] = useState<DriverDraft>(() => draftFromAgent());
  const [errors, setErrors] = useState<ReturnType<typeof validateDriver>>({});
  const [serverError, setServerError] = useState("");
  const [busy, setBusy] = useState(false);
  const computer = computers.find((c) => c.id === computerID);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const errs = validateDriver(draft);
    setErrors(errs);
    setServerError("");
    if (Object.keys(errs).length > 0 || !name) return;
    setBusy(true);
    try {
      const res = await api.createAgent(spaceSlug, {
        display_name: name,
        role_text: role.trim(),
        computer_id: computerID,
        driver_kind: draft.kind,
        driver_config: driverInput(draft),
      });
      onCreated(res.agent);
    } catch (err) {
      setServerError(fieldFromError(err));
      setBusy(false);
    }
  }

  const submitBtn = (
    <Button type="submit" form="agent-form" loading={busy} disabled={!name || !computerID}>
      Create agent
    </Button>
  );

  return (
    <form id="agent-form" onSubmit={submit} className="space-y-5">
      <div className="flex items-end gap-3">
        <PixelAvatar seed={name || "agent"} size="xl" />
        <div className="flex-1">
          <Field
            label="Name"
            value={name}
            onChange={(e) => setName(nameInput(e.target.value))}
            hint="Teammates @mention it by this name. Letters, digits, underscores."
            required
          />
        </div>
      </div>

      <div className="space-y-2">
        <TextArea label="Role" rows={3} value={role} onChange={(e) => setRole(e.target.value)} />
        <div className="flex flex-wrap gap-1.5">
          {ROLE_PRESETS.map((p) => (
            <button
              key={p.label}
              type="button"
              onClick={() => setRole(p.text)}
              className={`h-7 px-2.5 rounded-[7px] border text-[12.5px] transition-colors ${
                role === p.text ? "border-accent text-accent-text bg-accent-soft" : "border-line text-fg-3 hover:border-line-strong hover:text-fg"
              }`}
            >
              {p.label}
            </button>
          ))}
        </div>
      </div>

      {computers.length > 1 && (
        <Select label="Runs on" value={computerID} onChange={(e) => setComputerID(e.target.value)}>
          {computers.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name} · {c.connection_status}
            </option>
          ))}
        </Select>
      )}

      <div className="space-y-2">
        <div className="text-[13px] font-medium text-fg-2">Driver</div>
        <DriverFields draft={draft} onChange={setDraft} computer={computer} errors={errors} />
      </div>

      {serverError && (
        <div className="px-3.5 py-2.5 rounded-[10px] bg-danger-soft text-danger text-[13.5px] animate-fade-in">{serverError}</div>
      )}

      {footer ? footer(submitBtn) : <div className="flex justify-end">{submitBtn}</div>}
    </form>
  );
}
