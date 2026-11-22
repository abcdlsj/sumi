import { useCallback, useEffect, useState } from "react";
import { nameInput, type DriverConfigView, type DriverKind, type Member } from "./api";

/** Reusable agent creation preset stored locally in the browser; never stores API keys. */
export interface AgentTemplate {
  id: string;
  name: string;
  role_text: string;
  computer_id?: string;
  driver_kind: DriverKind;
  driver_config: Omit<DriverConfigView, "api_key_set">;
  created_at: string;
}

export type AgentTemplateInput = Omit<AgentTemplate, "id" | "created_at">;

const STORAGE_KEY = "sumi.agent_templates.v1";
const EVENT = "sumi:agent_templates";
const MAX_TEMPLATES = 50;

let memory: AgentTemplate[] | null = null;

function sanitizeDriverConfig(kind: DriverKind, raw?: DriverConfigView): Omit<DriverConfigView, "api_key_set"> {
  if (!raw) return {};
  if (kind === "acp") {
    const out: Omit<DriverConfigView, "api_key_set"> = {};
    if (raw.provider) out.provider = raw.provider;
    if (Array.isArray(raw.command) && raw.command.length > 0) out.command = raw.command.filter((x): x is string => typeof x === "string" && x.length > 0);
    if (raw.provider === "pi") {
      if (raw.pi_provider?.trim()) out.pi_provider = raw.pi_provider.trim();
      if (raw.pi_model?.trim()) out.pi_model = raw.pi_model.trim();
      if (Array.isArray(raw.pi_packages) && raw.pi_packages.length > 0) {
        out.pi_packages = raw.pi_packages.map((p) => p.trim()).filter(Boolean);
      }
    }
    return out;
  }
  const out: Omit<DriverConfigView, "api_key_set"> = {};
  if (raw.base_url?.trim()) out.base_url = raw.base_url.trim();
  if (raw.model?.trim()) out.model = raw.model.trim();
  return out;
}

function isValidTemplate(v: unknown): v is AgentTemplate {
  if (!v || typeof v !== "object") return false;
  const r = v as Record<string, unknown>;
  return (
    typeof r.id === "string" &&
    r.id.length > 0 &&
    typeof r.name === "string" &&
    r.name.trim().length > 0 &&
    typeof r.role_text === "string" &&
    (r.driver_kind === "acp" || r.driver_kind === "builtin")
  );
}

function loadAll(): AgentTemplate[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        const list = parsed
          .filter(isValidTemplate)
          .map((t) => ({
            ...t,
            driver_config: sanitizeDriverConfig(t.driver_kind, t.driver_config),
          }))
          .slice(0, MAX_TEMPLATES);
        memory = list;
        return list;
      }
    }
  } catch {
    // Fallback to in-memory list when localStorage is unavailable.
  }
  return memory ?? [];
}

function persist(list: AgentTemplate[]) {
  memory = list;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(list));
  } catch {
    // Ignore storage quota or private-mode errors.
  }
  window.dispatchEvent(new Event(EVENT));
}

function makeID(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `tpl-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

export function listAgentTemplates(): AgentTemplate[] {
  return loadAll();
}

export function saveAgentTemplate(input: AgentTemplateInput): AgentTemplate {
  const cleanName = input.name.trim().slice(0, 48);
  const existing = loadAll();
  const match = existing.find((t) => t.name.toLowerCase() === cleanName.toLowerCase());
  const next: AgentTemplate = {
    id: match?.id ?? makeID(),
    name: cleanName,
    role_text: input.role_text.trim(),
    computer_id: input.computer_id || undefined,
    driver_kind: input.driver_kind,
    driver_config: sanitizeDriverConfig(input.driver_kind, input.driver_config),
    created_at: new Date().toISOString(),
  };
  const merged = [next, ...existing.filter((t) => t.id !== next.id)].slice(0, MAX_TEMPLATES);
  persist(merged);
  return next;
}

export function updateAgentTemplate(id: string, input: AgentTemplateInput): AgentTemplate | null {
  const cleanName = input.name.trim().slice(0, 48);
  if (!cleanName) return null;
  const existing = loadAll();
  const idx = existing.findIndex((t) => t.id === id);
  if (idx < 0) return null;
  const updated: AgentTemplate = {
    ...existing[idx],
    name: cleanName,
    role_text: input.role_text.trim(),
    computer_id: input.computer_id || undefined,
    driver_kind: input.driver_kind,
    driver_config: sanitizeDriverConfig(input.driver_kind, input.driver_config),
  };
  const next = existing.map((t) => (t.id === id ? updated : t));
  persist(next);
  return updated;
}

export function deleteAgentTemplate(id: string) {
  const existing = loadAll();
  const next = existing.filter((t) => t.id !== id);
  if (next.length !== existing.length) persist(next);
}

export function clearAgentTemplatesForTests() {
  memory = null;
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore
  }
}

export function templateFromAgent(member: Member, templateName?: string): AgentTemplateInput {
  const ag = member.agent;
  const kind: DriverKind = ag?.driver_kind ?? "acp";
  return {
    name: (templateName ?? member.display_name).trim() || member.display_name,
    role_text: ag?.role_text ?? "",
    computer_id: ag?.computer_id,
    driver_kind: kind,
    driver_config: sanitizeDriverConfig(kind, ag?.driver_config),
  };
}

export function templateDriverSummary(tpl: Pick<AgentTemplate, "driver_kind" | "driver_config">): string {
  if (tpl.driver_kind === "acp") {
    const provider = tpl.driver_config.provider ?? "acp";
    if (provider === "pi" && tpl.driver_config.pi_model) return `pi · ${tpl.driver_config.pi_model}`;
    return provider;
  }
  return tpl.driver_config.model || "Model API";
}

/** Suggests a valid, non-colliding display name (max 32 chars, [A-Za-z0-9_]) when copying an agent. */
export function suggestCopyName(sourceName: string, existingNames: string[]): string {
  const taken = new Set(existingNames.map((n) => n.toLowerCase()));
  const cleaned = nameInput(sourceName) || "Agent";
  const stem = cleaned.replace(/_\d+$/, "") || cleaned;
  for (let n = 2; n < 1000; n++) {
    const suffix = `_${n}`;
    const candidate = `${stem.slice(0, Math.max(1, 32 - suffix.length))}${suffix}`;
    if (!taken.has(candidate.toLowerCase())) return candidate;
  }
  return `${stem.slice(0, 27)}_copy`;
}

export function useAgentTemplates() {
  const [templates, setTemplates] = useState<AgentTemplate[]>(() => loadAll());

  useEffect(() => {
    const sync = () => setTemplates(loadAll());
    window.addEventListener(EVENT, sync);
    window.addEventListener("storage", sync);
    return () => {
      window.removeEventListener(EVENT, sync);
      window.removeEventListener("storage", sync);
    };
  }, []);

  const save = useCallback((input: AgentTemplateInput) => saveAgentTemplate(input), []);
  const update = useCallback((id: string, input: AgentTemplateInput) => updateAgentTemplate(id, input), []);
  const remove = useCallback((id: string) => deleteAgentTemplate(id), []);

  return { templates, save, update, remove };
}
