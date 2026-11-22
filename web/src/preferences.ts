import { useCallback, useEffect, useState } from "react";

export type SendKeyPref = "enter" | "mod_enter";
export type ChannelSortPref = "default" | "unread" | "name";

const SEND_KEY = "sumi.pref.send_key";
const CHANNEL_SORT_KEY = "sumi.pref.channel_sort";
const SIDEBAR_COLLAPSED_KEY = "sumi.pref.sidebar_collapsed";
const TASK_DETAILS_COLLAPSED_KEY = "sumi.pref.task_details_collapsed";
const LAST_LOCATION_KEY = "sumi.pref.last_location";
const EVENT = "sumi:prefs";

export type SavedMainView =
  | { kind: "conversation"; convID: string }
  | { kind: "inbox" }
  | { kind: "tasks" }
  | { kind: "admin"; tab: "agents" | "computers" | "people" | "settings" };

interface LastLocationStore {
  spaceSlug?: string;
  views: Record<string, SavedMainView>;
}

const ADMIN_TABS = new Set(["agents", "computers", "people", "settings"]);
let lastLocationMemory: LastLocationStore | null = null;

function isValidSavedView(v: unknown): v is SavedMainView {
  if (!v || typeof v !== "object") return false;
  const r = v as Record<string, unknown>;
  if (r.kind === "inbox" || r.kind === "tasks") return true;
  if (r.kind === "conversation") return typeof r.convID === "string" && r.convID.length > 0;
  if (r.kind === "admin") return typeof r.tab === "string" && ADMIN_TABS.has(r.tab);
  return false;
}

function loadLastLocation(): LastLocationStore {
  const raw = readLS(LAST_LOCATION_KEY);
  if (raw) {
    try {
      const parsed = JSON.parse(raw) as Partial<LastLocationStore>;
      const views: Record<string, SavedMainView> = {};
      if (parsed && typeof parsed === "object" && parsed.views && typeof parsed.views === "object") {
        for (const [slug, view] of Object.entries(parsed.views)) {
          if (slug && isValidSavedView(view)) views[slug] = view;
        }
      }
      const next: LastLocationStore = {
        spaceSlug: typeof parsed?.spaceSlug === "string" && parsed.spaceSlug ? parsed.spaceSlug : undefined,
        views,
      };
      lastLocationMemory = next;
      return next;
    } catch {
      // ignore corrupted storage
    }
  }
  return lastLocationMemory ? { spaceSlug: lastLocationMemory.spaceSlug, views: { ...lastLocationMemory.views } } : { views: {} };
}

function saveLastLocation(next: LastLocationStore) {
  lastLocationMemory = { spaceSlug: next.spaceSlug, views: { ...next.views } };
  try {
    localStorage.setItem(LAST_LOCATION_KEY, JSON.stringify(lastLocationMemory));
  } catch {
    // ignore storage failures
  }
}

export function readLastSpace(): string {
  return loadLastLocation().spaceSlug ?? "";
}

export function writeLastSpace(spaceSlug: string) {
  if (!spaceSlug) return;
  const cur = loadLastLocation();
  if (cur.spaceSlug === spaceSlug) return;
  saveLastLocation({ ...cur, spaceSlug });
}

export function readLastSpaceView(spaceSlug: string): SavedMainView | null {
  if (!spaceSlug) return null;
  return loadLastLocation().views[spaceSlug] ?? null;
}

export function writeLastSpaceView(spaceSlug: string, view: SavedMainView) {
  if (!spaceSlug) return;
  const cur = loadLastLocation();
  saveLastLocation({
    spaceSlug,
    views: { ...cur.views, [spaceSlug]: view },
  });
}

export function clearLastLocation() {
  lastLocationMemory = null;
  try {
    localStorage.removeItem(LAST_LOCATION_KEY);
  } catch {
    // ignore storage failures
  }
}

function readLS(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeLS(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // ignore storage failures
  }
  window.dispatchEvent(new Event(EVENT));
}

export function isMacPlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  return /Mac|iPhone|iPad|iPod/i.test(navigator.platform || navigator.userAgent);
}

export function readSendKey(): SendKeyPref {
  return readLS(SEND_KEY) === "mod_enter" ? "mod_enter" : "enter";
}

export function useSendKey(): [SendKeyPref, (next: SendKeyPref) => void] {
  const [pref, setPref] = useState<SendKeyPref>(readSendKey);
  useEffect(() => {
    const sync = () => setPref(readSendKey());
    window.addEventListener(EVENT, sync);
    window.addEventListener("storage", sync);
    return () => {
      window.removeEventListener(EVENT, sync);
      window.removeEventListener("storage", sync);
    };
  }, []);
  const update = useCallback((next: SendKeyPref) => {
    setPref(next);
    writeLS(SEND_KEY, next);
  }, []);
  return [pref, update];
}

export function isComposerSendEvent(e: { key: string; shiftKey: boolean; metaKey: boolean; ctrlKey: boolean; nativeEvent?: { isComposing?: boolean } }, pref: SendKeyPref): boolean {
  if (e.key !== "Enter" || e.nativeEvent?.isComposing) return false;
  if (pref === "mod_enter") return e.metaKey || e.ctrlKey;
  return !e.shiftKey && !e.metaKey && !e.ctrlKey;
}

export function useChannelSort(): [ChannelSortPref, (next: ChannelSortPref) => void] {
  const read = (): ChannelSortPref => {
    const v = readLS(CHANNEL_SORT_KEY);
    return v === "unread" || v === "name" ? v : "default";
  };
  const [pref, setPref] = useState<ChannelSortPref>(read);
  const update = useCallback((next: ChannelSortPref) => {
    setPref(next);
    writeLS(CHANNEL_SORT_KEY, next);
  }, []);
  return [pref, update];
}

export function useSidebarCollapsed(): [boolean, (next: boolean | ((prev: boolean) => boolean)) => void] {
  const [collapsed, setCollapsed] = useState<boolean>(() => readLS(SIDEBAR_COLLAPSED_KEY) === "true");
  const update = useCallback((next: boolean | ((prev: boolean) => boolean)) => {
    setCollapsed((prev) => {
      const value = typeof next === "function" ? next(prev) : next;
      writeLS(SIDEBAR_COLLAPSED_KEY, value ? "true" : "false");
      return value;
    });
  }, []);
  return [collapsed, update];
}

export function useTaskDetailsCollapsed(): [boolean, (next: boolean | ((prev: boolean) => boolean)) => void] {
  const [collapsed, setCollapsed] = useState<boolean>(() => readLS(TASK_DETAILS_COLLAPSED_KEY) === "true");
  const update = useCallback((next: boolean | ((prev: boolean) => boolean)) => {
    setCollapsed((prev) => {
      const value = typeof next === "function" ? next(prev) : next;
      writeLS(TASK_DETAILS_COLLAPSED_KEY, value ? "true" : "false");
      return value;
    });
  }, []);
  return [collapsed, update];
}
