import { useCallback, useEffect, useState } from "react";

export type SendKeyPref = "enter" | "mod_enter";
export type ChannelSortPref = "default" | "unread" | "name";

const SEND_KEY = "sumi.pref.send_key";
const CHANNEL_SORT_KEY = "sumi.pref.channel_sort";
const SIDEBAR_COLLAPSED_KEY = "sumi.pref.sidebar_collapsed";
const EVENT = "sumi:prefs";

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
