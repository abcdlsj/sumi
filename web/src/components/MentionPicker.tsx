import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type KeyboardEvent, type RefObject } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, type BootstrapData, type Member } from "../api";
import { MemberAvatar } from "./Avatar";

/** `@name` opens the picker when it starts a word; names are letters, digits and underscores. */
const TRIGGER = /(?:^|[\s(])@([\p{L}\p{N}_]*)$/u;

type MentionInput = HTMLInputElement | HTMLTextAreaElement;

export interface MentionTrigger {
  query: string;
  /** Index of the `@` that opened the picker. */
  start: number;
}

export function detectMention(value: string, caret: number): MentionTrigger | null {
  const match = value.slice(0, caret).match(TRIGGER);
  if (!match) return null;
  return { query: match[1], start: caret - match[1].length - 1 };
}

/** Prefix hits first, then substring hits; agents before humans within each band. */
export function rankMentionCandidates(members: Member[], query: string): Member[] {
  const q = query.toLowerCase();
  const rank = (m: Member) => {
    const name = m.display_name.toLowerCase();
    const band = q === "" ? 0 : name.startsWith(q) ? 0 : name.includes(q) ? 1 : -1;
    if (band < 0) return -1;
    return band * 2 + (m.kind === "agent" ? 0 : 1);
  };
  return members
    .map((m) => ({ m, r: rank(m) }))
    .filter((x) => x.r >= 0)
    .sort((a, b) => a.r - b.r)
    .map((x) => x.m);
}

export interface MentionPickerOptions {
  members: Member[];
  /** The viewer is never a candidate. */
  selfID?: string;
  value: string;
  onChange: (value: string) => void;
  inputRef: RefObject<MentionInput | null>;
}

export interface MentionPicker {
  /** Candidates to draw; empty means no popup. */
  candidates: Member[];
  index: number;
  highlight: (i: number) => void;
  /** Re-reads the caret; call from `onChange`, `onClick` and `onKeyUp`. */
  detect: () => void;
  accept: (member: Member) => void;
  close: () => void;
  /** Call first in the composer's key handler; returns true when it consumed the key. */
  handleKeyDown: (e: KeyboardEvent<MentionInput>) => boolean;
  listID: string;
  optionID: (i: number) => string;
}

/** The `@` picker shared by every composer: opens on `@`, filters, moves, inserts. */
export function useMentionPicker({ members, selfID, value, onChange, inputRef }: MentionPickerOptions): MentionPicker {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null);
  const [index, setIndex] = useState(0);
  const caret = useRef<number | null>(null);
  const listID = useId();

  const candidates = useMemo(() => {
    if (!trigger) return [];
    return rankMentionCandidates(
      members.filter((m) => m.id !== selfID),
      trigger.query,
    );
  }, [members, selfID, trigger]);

  useEffect(() => setIndex((i) => (i >= candidates.length ? 0 : i)), [candidates.length]);

  // The composer re-renders with the inserted text; put the caret after it.
  useLayoutEffect(() => {
    if (caret.current === null) return;
    const el = inputRef.current;
    if (el) {
      el.focus();
      el.setSelectionRange(caret.current, caret.current);
    }
    caret.current = null;
  }, [value, inputRef]);

  const detect = useCallback(() => {
    const el = inputRef.current;
    if (!el) return;
    const next = detectMention(el.value, el.selectionStart ?? el.value.length);
    setTrigger(next);
    setIndex(0);
  }, [inputRef]);

  const close = useCallback(() => setTrigger(null), []);

  const accept = useCallback(
    (member: Member) => {
      if (!trigger) return;
      const el = inputRef.current;
      const pos = el?.selectionStart ?? value.length;
      const before = value.slice(0, trigger.start);
      const inserted = `@${member.display_name} `;
      onChange(before + inserted + value.slice(pos));
      caret.current = before.length + inserted.length;
      setTrigger(null);
    },
    [trigger, value, onChange, inputRef],
  );

  const handleKeyDown = useCallback(
    (e: KeyboardEvent<MentionInput>) => {
      if (!trigger || candidates.length === 0) return false;
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setIndex((i) => (i + 1) % candidates.length);
        return true;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setIndex((i) => (i - 1 + candidates.length) % candidates.length);
        return true;
      }
      if ((e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) || e.key === "Tab") {
        e.preventDefault();
        accept(candidates[index]);
        return true;
      }
      if (e.key === "Escape") {
        e.preventDefault();
        setTrigger(null);
        return true;
      }
      return false;
    },
    [trigger, candidates, index, accept],
  );

  return {
    candidates: trigger ? candidates : [],
    index,
    highlight: setIndex,
    detect,
    accept,
    close,
    handleKeyDown,
    listID,
    optionID: (i: number) => `${listID}-option-${i}`,
  };
}

/** Renders the popup above the composer's input line. */
export function MentionPopup({ picker }: { picker: MentionPicker }) {
  if (picker.candidates.length === 0) return null;
  return (
    <div
      role="listbox"
      aria-label="Mention candidates"
      className="absolute left-2 bottom-full mb-2 z-30 w-[300px] max-h-[248px] overflow-y-auto scroll-thin bg-card border border-line rounded-[8px] shadow-lg p-1 animate-pop-in"
    >
      {picker.candidates.map((m, i) => (
        <div
          key={m.id}
          id={picker.optionID(i)}
          role="option"
          aria-selected={i === picker.index}
          tabIndex={-1}
          onMouseEnter={() => picker.highlight(i)}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => picker.accept(m)}
          className={`h-9 px-2 rounded-[6px] flex items-center gap-2 cursor-pointer select-none ${
            i === picker.index ? "bg-accent-soft" : "hover:bg-subtle"
          }`}
        >
          <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" state={m.kind === "agent" ? m.agent?.runtime_state : undefined} />
          <span className="min-w-0 flex-1 truncate text-[13.5px] text-fg">{m.display_name}</span>
          <span className="shrink-0 text-[11.5px] text-fg-4">{m.kind === "agent" ? "Agent" : m.access_level}</span>
        </div>
      ))}
    </div>
  );
}

/**
 * Who can be mentioned here: the peer for a DM, the parent conversation's roster for a
 * thread or task (the API resolves those to the parent), and the channel roster otherwise.
 */
export function useMentionMembers(
  spaceSlug: string,
  boot: BootstrapData,
  convID: string | undefined,
  dmPeerID?: string,
): Member[] {
  const membersQ = useQuery({
    queryKey: ["conv-members", spaceSlug, convID],
    queryFn: () => api.listConversationMembers(spaceSlug, convID!),
    enabled: Boolean(convID) && !dmPeerID,
  });
  if (dmPeerID) return boot.members.filter((m) => m.id === dmPeerID);
  return membersQ.data?.members ?? [];
}
