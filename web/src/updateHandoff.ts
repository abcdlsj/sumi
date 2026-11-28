import { restoreDrafts, type ComposerDraft } from "./drafts";
import { writeLastSpace, writeLastSpaceView, type SavedMainView } from "./preferences";
import { consumeDesktopUpdateHandoff, getDesktopBridge } from "./shell";
import type { DetailTarget } from "./components/DetailPane";

/**
 * Participant in the zero-data-loss restart barrier.
 * Composers, detail panes, and scrollers register flush/guard/snapshot callbacks so
 * restarting for an update never discards unsaved drafts or scroll position.
 */
export interface RestartParticipant {
  id: string;
  /** Returns a human-readable reason when restart should wait (e.g. active upload). */
  getBlockingReason?: () => string | null;
  /** Flushes any pending in-memory edits before capturing state. */
  flushBeforeRestart?: () => Promise<void>;
  /** Captures ephemeral UI state to merge into update-handoff.json. */
  captureState?: () => Record<string, unknown>;
}

export interface SumiHandoffAppState {
  space_slug?: string;
  main_view?: SavedMainView;
  detail?: DetailTarget | null;
  scroll_conv_id?: string;
  scroll_top?: number;
  drafts?: Record<string, ComposerDraft>;
  reply_box_draft?: { key: string; text: string };
}

export class FlushRegistry {
  private readonly participants = new Map<string, RestartParticipant>();

  register(participant: RestartParticipant): () => void {
    this.participants.set(participant.id, participant);
    return () => {
      if (this.participants.get(participant.id) === participant) {
        this.participants.delete(participant.id);
      }
    };
  }

  getBlockingReasons(): string[] {
    const reasons: string[] = [];
    for (const p of this.participants.values()) {
      const reason = p.getBlockingReason?.();
      if (reason) reasons.push(reason);
    }
    return reasons;
  }

  /**
   * Flushes all registered participants in parallel. If any participant fails,
   * throws so restart is aborted and unsaved edits remain intact.
   */
  async flushAll(): Promise<void> {
    const tasks: Promise<void>[] = [];
    for (const p of this.participants.values()) {
      if (p.flushBeforeRestart) {
        tasks.push(p.flushBeforeRestart());
      }
    }
    await Promise.all(tasks);
  }

  captureAll(): Record<string, unknown> {
    const merged: Record<string, unknown> = {};
    for (const p of this.participants.values()) {
      if (p.captureState) {
        Object.assign(merged, p.captureState());
      }
    }
    return merged;
  }
}

export const updateFlushRegistry = new FlushRegistry();

const pendingScroll = new Map<string, number>();
const pendingReplyDrafts = new Map<string, string>();
let startupHandoff: SumiHandoffAppState | null = null;
let handoffConsumed = false;
const listeners = new Set<(state: SumiHandoffAppState) => void>();

export function setPendingScrollRestore(streamKey: string, scrollTop: number): void {
  if (!streamKey || !Number.isFinite(scrollTop) || scrollTop < 0) return;
  pendingScroll.set(streamKey, scrollTop);
}

export function takePendingScrollRestore(streamKey: string): number | null {
  const value = pendingScroll.get(streamKey);
  if (value === undefined) return null;
  pendingScroll.delete(streamKey);
  return value;
}

export function setPendingReplyDraft(key: string, text: string): void {
  if (!key || !text.trim()) return;
  pendingReplyDrafts.set(key, text);
}

export function takePendingReplyDraft(key: string): string | null {
  const value = pendingReplyDrafts.get(key);
  if (value === undefined) return null;
  pendingReplyDrafts.delete(key);
  return value;
}

export function readStartupHandoff(): SumiHandoffAppState | null {
  return startupHandoff;
}

export function subscribeStartupHandoff(fn: (state: SumiHandoffAppState) => void): () => void {
  listeners.add(fn);
  if (startupHandoff) {
    fn(startupHandoff);
  }
  return () => {
    listeners.delete(fn);
  };
}

function isValidSavedMainView(v: unknown): v is SavedMainView {
  if (!v || typeof v !== "object") return false;
  const r = v as Record<string, unknown>;
  if (r.kind === "inbox" || r.kind === "tasks") return true;
  if (r.kind === "conversation") return typeof r.convID === "string" && r.convID.length > 0;
  if (r.kind === "admin") {
    return r.tab === "agents" || r.tab === "computers" || r.tab === "people" || r.tab === "settings";
  }
  return false;
}

function isValidDetailTarget(v: unknown): v is DetailTarget {
  if (!v || typeof v !== "object") return false;
  const r = v as Record<string, unknown>;
  switch (r.kind) {
    case "task":
      return typeof r.taskID === "string" && r.taskID.length > 0;
    case "thread":
      return (
        typeof r.threadConvID === "string" &&
        r.threadConvID.length > 0 &&
        typeof r.rootMessageID === "string" &&
        typeof r.parentSlug === "string"
      );
    case "agent":
    case "human":
      return typeof r.memberID === "string" && r.memberID.length > 0;
    case "members":
    case "channel_tasks":
      return typeof r.convID === "string" && r.convID.length > 0;
    default:
      return false;
  }
}

export function applyStartupHandoffState(raw: Record<string, unknown>): SumiHandoffAppState {
  const parsed: SumiHandoffAppState = {};

  if (typeof raw.space_slug === "string" && raw.space_slug) {
    parsed.space_slug = raw.space_slug;
    writeLastSpace(raw.space_slug);
  }

  if (isValidSavedMainView(raw.main_view)) {
    parsed.main_view = raw.main_view;
    if (parsed.space_slug) {
      writeLastSpaceView(parsed.space_slug, raw.main_view);
    }
  }

  if (isValidDetailTarget(raw.detail)) {
    parsed.detail = raw.detail;
  }

  if (typeof raw.scroll_conv_id === "string" && typeof raw.scroll_top === "number" && Number.isFinite(raw.scroll_top)) {
    parsed.scroll_conv_id = raw.scroll_conv_id;
    parsed.scroll_top = raw.scroll_top;
    setPendingScrollRestore(raw.scroll_conv_id, raw.scroll_top);
  }

  if (raw.drafts && typeof raw.drafts === "object" && !Array.isArray(raw.drafts)) {
    const drafts: Record<string, ComposerDraft> = {};
    for (const [convID, val] of Object.entries(raw.drafts as Record<string, unknown>)) {
      if (val && typeof val === "object") {
        const entry = val as Record<string, unknown>;
        if (typeof entry.text === "string") {
          drafts[convID] = {
            text: entry.text,
            replyTo: typeof entry.replyTo === "string" ? entry.replyTo : null,
          };
        }
      }
    }
    if (Object.keys(drafts).length > 0) {
      parsed.drafts = drafts;
      restoreDrafts(drafts);
    }
  }

  if (raw.reply_box_draft && typeof raw.reply_box_draft === "object") {
    const r = raw.reply_box_draft as Record<string, unknown>;
    if (typeof r.key === "string" && typeof r.text === "string" && r.text.trim()) {
      parsed.reply_box_draft = { key: r.key, text: r.text };
      setPendingReplyDraft(r.key, r.text);
    }
  }

  startupHandoff = parsed;
  for (const fn of listeners) {
    fn(parsed);
  }
  return parsed;
}

/**
 * Consumes the one-shot update-handoff.json from the desktop control server on
 * startup (strictly a no-op in regular browsers).
 */
export async function consumeStartupHandoff(): Promise<SumiHandoffAppState | null> {
  if (handoffConsumed) return startupHandoff;
  const bridge = getDesktopBridge();
  if (!bridge) return null;
  handoffConsumed = true;
  try {
    const payload = await consumeDesktopUpdateHandoff(bridge);
    if (!payload || !payload.app_state) return null;
    return applyStartupHandoffState(payload.app_state);
  } catch {
    return null;
  }
}

export function clearUpdateHandoffForTests(): void {
  pendingScroll.clear();
  pendingReplyDrafts.clear();
  startupHandoff = null;
  handoffConsumed = false;
  listeners.clear();
}
