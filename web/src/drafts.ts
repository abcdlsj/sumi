import { useEffect, useState } from "react";

export interface ComposerDraft {
  text: string;
  replyTo: string | null;
}

const STORAGE_KEY = "sumi.drafts.v1";
const EVENT = "sumi:drafts";
const memory = new Map<string, ComposerDraft>();

function loadAll(): Record<string, ComposerDraft> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as Record<string, ComposerDraft>;
      if (parsed && typeof parsed === "object") return parsed;
    }
  } catch {
    // Fallback to in-memory map when localStorage is unavailable.
  }
  const out: Record<string, ComposerDraft> = {};
  for (const [k, v] of memory) out[k] = v;
  return out;
}

function saveAll(all: Record<string, ComposerDraft>) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(all));
  } catch {
    // ignore storage quota / private mode failures
  }
}

export function readDraft(convID: string): ComposerDraft | undefined {
  const mem = memory.get(convID);
  if (mem) return mem;
  const stored = loadAll()[convID];
  if (stored) {
    memory.set(convID, stored);
    return stored;
  }
  return undefined;
}

export function writeDraft(convID: string, draft: ComposerDraft) {
  const empty = !draft.text.trim() && !draft.replyTo;
  const prev = memory.get(convID);
  const hadDraft = Boolean(prev?.text.trim() || prev?.replyTo);
  if (empty) {
    memory.delete(convID);
  } else {
    memory.set(convID, draft);
  }
  const all = loadAll();
  if (empty) delete all[convID];
  else all[convID] = draft;
  saveAll(all);
  if (hadDraft !== !empty) {
    window.dispatchEvent(new Event(EVENT));
  }
}

export function clearAllDraftsForTests() {
  memory.clear();
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // ignore
  }
}

/** Reactive set of conversation IDs that currently hold a non-empty unsaved text draft. */
export function useDraftConvIDs(): Set<string> {
  const compute = () => {
    const all = loadAll();
    const ids = new Set<string>();
    for (const [id, d] of Object.entries(all)) {
      if (d?.text?.trim()) ids.add(id);
    }
    for (const [id, d] of memory) {
      if (d?.text?.trim()) ids.add(id);
      else ids.delete(id);
    }
    return ids;
  };
  const [ids, setIds] = useState<Set<string>>(compute);
  useEffect(() => {
    const sync = () => setIds(compute());
    window.addEventListener(EVENT, sync);
    window.addEventListener("storage", sync);
    return () => {
      window.removeEventListener(EVENT, sync);
      window.removeEventListener("storage", sync);
    };
  }, []);
  return ids;
}
