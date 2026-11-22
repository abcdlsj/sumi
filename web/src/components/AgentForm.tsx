import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { Bookmark, Bot, ChevronRight, Cpu, Eye, EyeOff, KeyRound, Link2, Monitor, Pencil, Terminal, X } from "lucide-react";
import {
  suggestCopyName,
  templateDriverSummary,
  updateAgentTemplate,
  useAgentTemplates,
  type AgentTemplate,
} from "../agentTemplates";
import {
  api,
  ApiError,
  nameInput,
  type ACPProvider,
  type AgentCreateProposal,
  type Computer,
  type DriverConfigInput,
  type DriverKind,
  type Member,
} from "../api";
import { MemberAvatar, PixelAvatar } from "./Avatar";
import { Button, Field, Modal, Select, TextArea, useToast } from "./ui";

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

export function draftFromAgent(m?: Member, computer?: Computer): DriverDraft {
  const cfg = m?.agent?.driver_config;
  const defaultProvider: ACPProvider =
    cfg?.provider ?? (computer?.acp_providers && computer.acp_providers.length > 0 ? computer.acp_providers[0] : "pi");
  return {
    kind: m?.agent?.driver_kind ?? "acp",
    provider: defaultProvider,
    command: cfg?.command?.map((a) => (/\s/.test(a) ? JSON.stringify(a) : a)).join(" ") ?? "",
    piProvider: cfg?.pi_provider ?? "",
    piModel: cfg?.pi_model ?? "",
    piPackages: cfg?.pi_packages?.join(", ") ?? "",
    baseURL: cfg?.base_url ?? "",
    model: cfg?.model ?? "",
    apiKey: "",
  };
}

export function draftFromTemplate(tpl: AgentTemplate, computer?: Computer): DriverDraft {
  const cfg = tpl.driver_config;
  const defaultProvider: ACPProvider =
    cfg.provider ?? (computer?.acp_providers && computer.acp_providers.length > 0 ? computer.acp_providers[0] : "pi");
  return {
    kind: tpl.driver_kind,
    provider: defaultProvider,
    command: cfg.command?.map((a) => (/\s/.test(a) ? JSON.stringify(a) : a)).join(" ") ?? "",
    piProvider: cfg.pi_provider ?? "",
    piModel: cfg.pi_model ?? "",
    piPackages: cfg.pi_packages?.join(", ") ?? "",
    baseURL: cfg.base_url ?? "",
    model: cfg.model ?? "",
    apiKey: "",
  };
}

/** A template may leave fields blank; blanks never wipe what the form already holds. */
export function mergeDraft(current: DriverDraft, fromTemplate: DriverDraft): DriverDraft {
  const keep = (from: string, fallback: string) => (from.trim() ? from : fallback);
  return {
    kind: fromTemplate.kind,
    provider: fromTemplate.provider,
    command: keep(fromTemplate.command, current.command),
    piProvider: keep(fromTemplate.piProvider, current.piProvider),
    piModel: keep(fromTemplate.piModel, current.piModel),
    piPackages: keep(fromTemplate.piPackages, current.piPackages),
    baseURL: keep(fromTemplate.baseURL, current.baseURL),
    model: keep(fromTemplate.model, current.model),
    // Templates never carry a key; a key already typed into the form stays.
    apiKey: current.apiKey,
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
      className={`flex-1 text-left p-3.5 rounded-[8px] border transition-[border-color,background-color,box-shadow] duration-150 ${
        active ? "border-accent bg-accent-soft shadow-[var(--ring)]" : "border-line-strong bg-card hover:border-fg-4"
      }`}
    >
      <div className="flex items-center gap-2">
        <span className={`size-7 rounded-[6px] flex items-center justify-center [&>svg]:size-4 ${active ? "bg-accent text-accent-fg" : "bg-subtle text-fg-2"}`}>
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
  showApiKey = true,
}: {
  draft: DriverDraft;
  onChange: (d: DriverDraft) => void;
  computer?: Computer;
  errors?: Partial<Record<"baseURL" | "model" | "provider" | "command", string>>;
  apiKeySet?: boolean;
  showApiKey?: boolean;
}) {
  const [showKey, setShowKey] = useState(false);
  const hasAdvancedValues = Boolean(
    draft.command.trim() || draft.piProvider.trim() || draft.piModel.trim() || draft.piPackages.trim(),
  );
  const [showAdvanced, setShowAdvanced] = useState(hasAdvancedValues);
  useEffect(() => {
    if (hasAdvancedValues) setShowAdvanced(true);
  }, [hasAdvancedValues]);
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
                  className={`w-full px-3.5 py-2.5 rounded-[7px] border flex items-center gap-3 text-left transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
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
          <div className="pt-0.5">
            <button
              type="button"
              onClick={() => setShowAdvanced((v) => !v)}
              aria-expanded={showAdvanced}
              className="inline-flex items-center gap-1.5 text-[13px] font-medium text-fg-3 hover:text-fg transition-colors"
            >
              <ChevronRight className={`size-3.5 transition-transform duration-150 ${showAdvanced ? "rotate-90" : ""}`} />
              <span>Advanced</span>
              {hasAdvancedValues && !showAdvanced && <span className="size-1.5 rounded-full bg-accent" />}
            </button>
          </div>
          {showAdvanced && (
            <div className="space-y-3.5 rounded-[7px] border border-line bg-subtle/40 p-3.5 animate-fade-in">
              <Field
                label="Custom command"
                optional
                leading={<Terminal />}
                value={draft.command}
                onChange={(e) => set({ command: e.target.value })}
                placeholder="npx -y my-acp-agent --stdio"
                hint="Overrides the default ACP launcher command. Leave empty to use the provider default."
                error={errors.command}
                autoComplete="off"
                spellCheck={false}
              />
              {draft.provider === "pi" && (
                <div className="space-y-3 pt-2 border-t border-line">
                  <p className="text-[12.5px] text-fg-3 leading-snug">
                    Optional pi isolation overrides. Leave empty to follow the host pi settings.
                  </p>
                  <Field
                    label="pi provider"
                    optional
                    leading={<Bot />}
                    value={draft.piProvider}
                    onChange={(e) => set({ piProvider: e.target.value })}
                    placeholder="openai"
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
                    placeholder="gpt-5-mini"
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
                    placeholder="npm:my-pi-provider"
                    hint="Comma-separated npm: specs loaded in the isolated pi directory."
                    autoComplete="off"
                    spellCheck={false}
                  />
                </div>
              )}
            </div>
          )}
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
          {showApiKey ? (
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
          ) : (
            <p className="text-[12.5px] text-fg-3">API keys are never stored in templates — enter one when creating an agent.</p>
          )}
        </div>
      )}
    </div>
  );
}

function fieldFromError(err: unknown): string {
  return err instanceof ApiError ? err.message : err instanceof Error ? err.message : "Could not create agent";
}

/** Modal editor for updating a saved agent template's name, role, preferred computer, and driver. */
export function AgentTemplateDialog({
  open,
  template,
  computers,
  onClose,
  onSaved,
}: {
  open: boolean;
  template: AgentTemplate | null;
  computers: Computer[];
  onClose: () => void;
  onSaved?: (updated: AgentTemplate) => void;
}) {
  const toast = useToast();
  const [name, setName] = useState("");
  const [role, setRole] = useState("");
  const [computerID, setComputerID] = useState("");
  const [draft, setDraft] = useState<DriverDraft>(() => draftFromAgent(undefined, computers[0]));
  const [errors, setErrors] = useState<ReturnType<typeof validateDriver>>({});

  useEffect(() => {
    if (!open || !template) return;
    const compID = template.computer_id && computers.some((c) => c.id === template.computer_id) ? template.computer_id : "";
    const comp = computers.find((c) => c.id === compID) ?? computers[0];
    setName(template.name);
    setRole(template.role_text);
    setComputerID(compID);
    setDraft(draftFromTemplate(template, comp));
    setErrors({});
  }, [open, template, computers]);

  const computer = computers.find((c) => c.id === computerID) ?? computers[0];

  function submit(e: FormEvent) {
    e.preventDefault();
    e.stopPropagation();
    if (!template) return;
    const cleanName = name.trim();
    const errs = validateDriver(draft);
    setErrors(errs);
    if (!cleanName || Object.keys(errs).length > 0) return;
    const updated = updateAgentTemplate(template.id, {
      name: cleanName,
      role_text: role,
      computer_id: computerID || undefined,
      driver_kind: draft.kind,
      driver_config: driverInput(draft),
    });
    if (updated) {
      toast(`Updated template “${updated.name}”`, "success");
      onSaved?.(updated);
    }
    onClose();
  }

  return (
    <Modal open={open && Boolean(template)} onClose={onClose} label="Edit template" title="Edit template" description="Update this saved agent preset for future agents." width={560}>
      <form onSubmit={submit} className="px-6 pb-6 pt-4 space-y-5">
        <Field label="Template name" autoFocus value={name} onChange={(e) => setName(e.target.value)} maxLength={48} required />
        <div className="space-y-2">
          <TextArea label="Role" rows={3} value={role} onChange={(e) => setRole(e.target.value)} />
          <div className="flex flex-wrap gap-1.5">
            {ROLE_PRESETS.map((p) => (
              <button
                key={p.label}
                type="button"
                onClick={() => setRole(p.text)}
                className={`h-7 px-2.5 rounded-[5px] border text-[12.5px] transition-colors ${
                  role === p.text ? "border-accent text-accent-text bg-accent-soft" : "border-line text-fg-3 hover:border-line-strong hover:text-fg"
                }`}
              >
                {p.label}
              </button>
            ))}
          </div>
        </div>
        {computers.length > 1 && (
          <Select
            label="Default computer"
            optional
            value={computerID}
            onChange={setComputerID}
            options={[
              { value: "", label: "Any available computer" },
              ...computers.map((c) => ({
                value: c.id,
                label: c.name,
                description: `${c.hostname} · ${c.os === "macos" ? "macOS" : "Linux"}`,
                icon: <Monitor className="size-4 text-fg-3" />,
              })),
            ]}
          />
        )}
        <div className="space-y-2">
          <div className="text-[13px] font-medium text-fg-2">Driver</div>
          <DriverFields draft={draft} onChange={setDraft} computer={computer} errors={errors} showApiKey={false} />
        </div>
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={!name.trim()}>
            Save changes
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function AgentForm({
  spaceSlug,
  computers,
  initialAgent,
  initialProposal,
  existingMembers = [],
  onCreated,
  footer,
}: {
  spaceSlug: string;
  computers: Computer[];
  initialAgent?: Member;
  initialProposal?: AgentCreateProposal;
  existingMembers?: Member[];
  onCreated: (agent: Member) => void;
  footer?: (submit: ReactNode) => ReactNode;
}) {
  const toast = useToast();
  const { templates, save: saveTemplate, update: updateTemplate, remove: removeTemplate } = useAgentTemplates();
  const existingAgents = existingMembers.filter((m) => m.kind === "agent" && m.agent);
  const existingNames = existingMembers.map((m) => m.display_name);

  const resolveComputerID = (preferred?: string) =>
    (preferred && computers.some((c) => c.id === preferred) ? preferred : "") ||
    computers.find((c) => c.connection_status === "online")?.id ||
    computers[0]?.id ||
    "";

  const proposalParams = initialProposal?.params;
  const [name, setName] = useState(() => {
    if (proposalParams?.display_name) return nameInput(proposalParams.display_name) || "Lin";
    if (initialAgent) return suggestCopyName(initialAgent.display_name, existingNames);
    return "Lin";
  });
  const [nameTouched, setNameTouched] = useState(() => Boolean(proposalParams?.display_name));
  const [role, setRole] = useState(() =>
    proposalParams?.role_text !== undefined
      ? proposalParams.role_text
      : (initialAgent?.agent?.role_text ?? ROLE_PRESETS[0].text),
  );
  const [computerID, setComputerID] = useState(() => resolveComputerID(proposalParams?.computer_id ?? initialAgent?.agent?.computer_id));
  const initialComputer = computers.find((c) => c.id === computerID);
  const [draft, setDraft] = useState<DriverDraft>(() => {
    if (proposalParams?.driver_kind) {
      return draftFromTemplate(
        {
          id: "",
          name: "",
          role_text: proposalParams.role_text ?? "",
          computer_id: proposalParams.computer_id,
          driver_kind: proposalParams.driver_kind,
          driver_config: proposalParams.driver_config ?? {},
          created_at: "",
        },
        initialComputer,
      );
    }
    return draftFromAgent(initialAgent, initialComputer);
  });
  const [sourceAgentID, setSourceAgentID] = useState(() => initialAgent?.id ?? "");
  const [activeTemplateID, setActiveTemplateID] = useState("");
  const [apiKeyNotice, setApiKeyNotice] = useState(() =>
    initialAgent?.agent?.driver_kind === "builtin" && initialAgent.agent.driver_config?.api_key_set ? initialAgent.display_name : "",
  );
  const [savingTemplate, setSavingTemplate] = useState(false);
  const [editingTemplate, setEditingTemplate] = useState<AgentTemplate | null>(null);
  const [templateName, setTemplateName] = useState("");
  const [errors, setErrors] = useState<ReturnType<typeof validateDriver>>({});
  const [serverError, setServerError] = useState("");
  const [busy, setBusy] = useState(false);
  const computer = computers.find((c) => c.id === computerID);
  const activeTemplate = templates.find((t) => t.id === activeTemplateID) ?? null;

  function applyAgentCopy(agentID: string) {
    setSourceAgentID(agentID);
    setActiveTemplateID("");
    setErrors({});
    setServerError("");
    const src = existingAgents.find((m) => m.id === agentID);
    if (!src) {
      setApiKeyNotice("");
      return;
    }
    const nextCompID = resolveComputerID(src.agent?.computer_id);
    const nextComp = computers.find((c) => c.id === nextCompID);
    setComputerID(nextCompID);
    setRole(src.agent?.role_text ?? "");
    setDraft(draftFromAgent(src, nextComp));
    if (!nameTouched) setName(suggestCopyName(src.display_name, existingNames));
    setApiKeyNotice(src.agent?.driver_kind === "builtin" && src.agent.driver_config?.api_key_set ? src.display_name : "");
  }

  function applyTemplate(tpl: AgentTemplate) {
    setActiveTemplateID(tpl.id);
    setSourceAgentID("");
    setApiKeyNotice("");
    setErrors({});
    setServerError("");
    // Blank template fields are left undefined on purpose so switching templates
    // fills in what the template defines and keeps everything else intact.
    const pinned = computers.find((c) => c.id === tpl.computer_id);
    if (pinned) setComputerID(pinned.id);
    const comp = pinned ?? computers.find((c) => c.id === computerID);
    if (tpl.role_text.trim()) setRole(tpl.role_text);
    setDraft((cur) => mergeDraft(cur, draftFromTemplate(tpl, comp)));
  }

  function handleUpdateActiveTemplate() {
    if (!activeTemplate) return;
    if (draft.kind === "acp") {
      const parsed = splitCommandArgs(draft.command);
      if (parsed.error) {
        setErrors({ command: parsed.error });
        return;
      }
    }
    const updated = updateTemplate(activeTemplate.id, {
      name: activeTemplate.name,
      role_text: role,
      computer_id: computerID || undefined,
      driver_kind: draft.kind,
      driver_config: driverInput(draft),
    });
    if (updated) toast(`Updated template “${updated.name}”`, "success");
  }

  function handleSaveTemplate() {
    const clean = templateName.trim();
    if (!clean) return;
    if (draft.kind === "acp") {
      const parsed = splitCommandArgs(draft.command);
      if (parsed.error) {
        setErrors({ command: parsed.error });
        return;
      }
    }
    const saved = saveTemplate({
      name: clean,
      role_text: role,
      computer_id: computerID || undefined,
      driver_kind: draft.kind,
      driver_config: driverInput(draft),
    });
    setActiveTemplateID(saved.id);
    setSavingTemplate(false);
    setTemplateName("");
    toast(`Saved template “${saved.name}”`, "success");
  }

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
        action_card_message_id: initialProposal?.message_id,
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
    <>
    <form id="agent-form" onSubmit={submit} className="space-y-5">
      {(existingAgents.length > 0 || templates.length > 0) && (
        <div className="space-y-3 pb-4 border-b border-line">
          {existingAgents.length > 0 && (
            <Select
              size="sm"
              label="Copy from agent"
              optional
              searchable={existingAgents.length > 5}
              searchPlaceholder="Filter agents…"
              placeholder="Choose an existing agent…"
              value={sourceAgentID}
              onChange={applyAgentCopy}
              options={existingAgents.map((m) => ({
                value: m.id,
                label: m.display_name,
                description: m.agent?.role_text?.replace(/\s+/g, " ").trim() || undefined,
                icon: <MemberAvatar id={m.id} name={m.display_name} kind="agent" size="xs" state={m.agent?.runtime_state} />,
                trailing:
                  m.agent?.driver_kind === "acp"
                    ? m.agent.driver_config?.provider ?? "acp"
                    : m.agent?.driver_config?.model || "Model API",
              }))}
            />
          )}
          {templates.length > 0 && (
            <div className="space-y-1.5">
              <div className="text-[12.5px] font-medium text-fg-3">Templates</div>
              <div className="flex flex-wrap gap-1.5">
                {templates.map((tpl) => {
                  const active = activeTemplateID === tpl.id;
                  return (
                    <div
                      key={tpl.id}
                      className={`inline-flex items-center h-7 rounded-[5px] border text-[12.5px] transition-colors ${
                        active
                          ? "border-accent text-accent-text bg-accent-soft"
                          : "border-line text-fg-2 bg-subtle/50 hover:border-line-strong hover:text-fg"
                      }`}
                    >
                      <button
                        type="button"
                        onClick={() => applyTemplate(tpl)}
                        title={tpl.role_text || templateDriverSummary(tpl)}
                        className="h-full pl-2.5 pr-1.5 inline-flex items-center gap-1.5"
                      >
                        <span className="truncate max-w-[140px] font-medium">{tpl.name}</span>
                        <span className="font-mono text-[11px] opacity-65">{templateDriverSummary(tpl)}</span>
                      </button>
                      <button
                        type="button"
                        aria-label={`Edit template ${tpl.name}`}
                        title={`Edit ${tpl.name}`}
                        onClick={() => setEditingTemplate(tpl)}
                        className="h-full px-1 text-fg-4 hover:text-fg transition-colors inline-flex items-center"
                      >
                        <Pencil className="size-3" />
                      </button>
                      <button
                        type="button"
                        aria-label={`Delete template ${tpl.name}`}
                        title={`Delete ${tpl.name}`}
                        onClick={() => {
                          if (active) setActiveTemplateID("");
                          removeTemplate(tpl.id);
                        }}
                        className="h-full pl-1 pr-1.5 text-fg-4 hover:text-danger transition-colors inline-flex items-center"
                      >
                        <X className="size-3" />
                      </button>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>
      )}

      {apiKeyNotice && (
        <div className="px-3 py-2 rounded-[6px] border border-line bg-subtle/60 text-[12.5px] text-fg-2 leading-snug animate-fade-in">
          API key from <span className="font-medium text-fg">{apiKeyNotice}</span> is not copied for security — enter one below if needed.
        </div>
      )}

      <div className="flex items-start gap-3">
        <div className="pt-2.5 shrink-0">
          <PixelAvatar seed={name || "agent"} size="xl" />
        </div>
        <div className="flex-1 min-w-0">
          <Field
            label="Name"
            value={name}
            onChange={(e) => {
              setNameTouched(true);
              setName(nameInput(e.target.value));
            }}
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
              className={`h-7 px-2.5 rounded-[5px] border text-[12.5px] transition-colors ${
                role === p.text ? "border-accent text-accent-text bg-accent-soft" : "border-line text-fg-3 hover:border-line-strong hover:text-fg"
              }`}
            >
              {p.label}
            </button>
          ))}
        </div>
      </div>

      {computers.length > 1 && (
        <Select
          label="Runs on"
          value={computerID}
          onChange={setComputerID}
          options={computers.map((c) => ({
            value: c.id,
            label: c.name,
            description: `${c.hostname} · ${c.os === "macos" ? "macOS" : "Linux"}`,
            icon: <Monitor className="size-4 text-fg-3" />,
            trailing: (
              <span className="inline-flex items-center gap-1.5">
                <span className={`size-1.5 rounded-full ${c.connection_status === "online" ? "bg-online" : "bg-fg-4"}`} />
                {c.connection_status}
              </span>
            ),
          }))}
        />
      )}

      <div className="space-y-2">
        <div className="text-[13px] font-medium text-fg-2">Driver</div>
        <DriverFields draft={draft} onChange={setDraft} computer={computer} errors={errors} />
      </div>

      <div>
        {savingTemplate ? (
          <div className="rounded-[7px] border border-line bg-subtle/40 p-3 space-y-2 animate-fade-in">
            <div className="text-[12.5px] font-medium text-fg-2">Save current role &amp; driver as a template</div>
            <div className="flex items-center gap-2">
              <input
                autoFocus
                value={templateName}
                onChange={(e) => setTemplateName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    handleSaveTemplate();
                  } else if (e.key === "Escape") {
                    e.preventDefault();
                    setSavingTemplate(false);
                  }
                }}
                placeholder="e.g. Backend Reviewer"
                aria-label="Template name"
                maxLength={48}
                className="flex-1 h-8 px-2.5 rounded-[6px] border border-line-strong bg-card text-[13.5px] text-fg outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
              />
              <Button type="button" size="sm" variant="ghost" onClick={() => setSavingTemplate(false)}>
                Cancel
              </Button>
              <Button type="button" size="sm" variant="secondary" disabled={!templateName.trim()} onClick={handleSaveTemplate}>
                Save
              </Button>
            </div>
            {draft.kind === "builtin" && draft.apiKey && (
              <p className="text-[12px] text-fg-3">API keys are never saved in templates.</p>
            )}
          </div>
        ) : (
          <div className="flex items-center gap-4 flex-wrap">
            {activeTemplate && (
              <button
                type="button"
                onClick={handleUpdateActiveTemplate}
                className="inline-flex items-center gap-1.5 text-[13px] font-medium text-accent-text hover:underline underline-offset-2 transition-colors"
              >
                <Pencil className="size-3.5" />
                <span>Update “{activeTemplate.name}”</span>
              </button>
            )}
            <button
              type="button"
              onClick={() => {
                setTemplateName(name || "Agent template");
                setSavingTemplate(true);
              }}
              className="inline-flex items-center gap-1.5 text-[13px] font-medium text-fg-3 hover:text-fg transition-colors"
            >
              <Bookmark className="size-3.5" />
              <span>{activeTemplate ? "Save as new template" : "Save as template"}</span>
            </button>
          </div>
        )}
      </div>

      {serverError && (
        <div className="px-3.5 py-2.5 rounded-[6px] bg-danger-soft text-danger text-[13.5px] animate-fade-in">{serverError}</div>
      )}

      {footer ? footer(submitBtn) : <div className="flex justify-end">{submitBtn}</div>}
    </form>

    <AgentTemplateDialog
      open={Boolean(editingTemplate)}
      template={editingTemplate}
      computers={computers}
      onClose={() => setEditingTemplate(null)}
      onSaved={(updated) => {
        if (activeTemplateID === updated.id) applyTemplate(updated);
      }}
    />
    </>
  );
}
