import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { createPortal } from "react-dom";
import { useQueryClient } from "@tanstack/react-query";
import { SmilePlus } from "lucide-react";
import { api, type Member, type Message, type ReactionGroup } from "../api";
import { getReactionEmoji, REACTION_EMOJIS, type ReactionEmoji } from "../emojis";
import { useAction, useFloatingPosition } from "./ui";

export function EmojiIcon({ emoji, size = 13, className = "" }: { emoji: string; size?: number; className?: string }) {
  const meta = getReactionEmoji(emoji);
  return (
    <img
      src={meta.src}
      alt=""
      aria-hidden="true"
      width={size}
      height={size}
      draggable={false}
      className={`inline-block shrink-0 select-none ${className}`}
    />
  );
}

function applyOptimisticReaction(
  groups: ReactionGroup[] | undefined,
  emoji: ReactionEmoji,
  removing: boolean,
  selfID?: string,
): ReactionGroup[] {
  const list = (groups ?? []).map((g) => ({ ...g, member_ids: [...g.member_ids] }));
  const idx = list.findIndex((g) => g.emoji === emoji);
  if (removing) {
    if (idx < 0) return list;
    const g = list[idx];
    const nextCount = Math.max(0, g.count - 1);
    const nextMembers = selfID ? g.member_ids.filter((id) => id !== selfID) : g.member_ids;
    if (nextCount === 0) list.splice(idx, 1);
    else list[idx] = { ...g, count: nextCount, member_ids: nextMembers, reacted: false };
    return list;
  }
  if (idx >= 0) {
    const g = list[idx];
    const nextMembers = selfID && !g.member_ids.includes(selfID) ? [...g.member_ids, selfID] : g.member_ids;
    list[idx] = { ...g, count: g.count + 1, member_ids: nextMembers, reacted: true };
  } else {
    list.push({ emoji, count: 1, member_ids: selfID ? [selfID] : [], reacted: true });
  }
  return list;
}

/** Toggles a reaction optimistically and reconciles with the server response. */
export function useReactionToggle(
  spaceSlug: string,
  convID?: string,
  onPatched?: (messageID: string, reactions: ReactionGroup[]) => void,
  selfID?: string,
) {
  const qc = useQueryClient();
  const act = useAction();

  const toggle = useCallback(
    async (messageID: string, emoji: ReactionEmoji, currentlyReacted: boolean) => {
      type CachePage = { messages: Message[]; read_seq: number; following: boolean; has_more?: boolean };
      const key = convID ? ["messages", spaceSlug, convID] : null;
      const prevPage = key ? qc.getQueryData<CachePage>(key) : undefined;
      const prevMsg = prevPage?.messages.find((m) => m.id === messageID);
      const optimistic = applyOptimisticReaction(prevMsg?.reactions, emoji, currentlyReacted, selfID);

      if (key && prevPage && prevMsg) {
        qc.setQueryData<CachePage>(key, {
          ...prevPage,
          messages: prevPage.messages.map((m) => (m.id === messageID ? { ...m, reactions: optimistic } : m)),
        });
      }
      onPatched?.(messageID, optimistic);

      await act.run(async () => {
        try {
          const res = currentlyReacted
            ? await api.removeReaction(spaceSlug, messageID, emoji)
            : await api.addReaction(spaceSlug, messageID, emoji);
          const next = res.reactions ?? [];
          if (key) {
            qc.setQueryData<CachePage>(key, (old) =>
              old
                ? {
                    ...old,
                    messages: old.messages.map((m) => (m.id === messageID ? { ...m, reactions: next } : m)),
                  }
                : old,
            );
          }
          onPatched?.(messageID, next);
        } catch (err) {
          if (key && prevPage) qc.setQueryData<CachePage>(key, prevPage);
          if (prevMsg) onPatched?.(messageID, prevMsg.reactions ?? []);
          throw err;
        }
      });
    },
    [act, convID, onPatched, qc, selfID, spaceSlug],
  );

  return { toggle, busy: act.busy };
}

function ReactionPopover({
  triggerRef,
  reactedSet,
  align = "right",
  onPick,
  onClose,
}: {
  triggerRef: RefObject<HTMLElement | null>;
  reactedSet: Set<string>;
  align?: "left" | "right";
  onPick: (emoji: ReactionEmoji) => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const style = useFloatingPosition(true, triggerRef, ref, { align, width: 256, maxHeight: 60 });

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (ref.current?.contains(target) || triggerRef.current?.contains(target)) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        onClose();
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey, true);
    };
  }, [onClose, triggerRef]);

  return createPortal(
    <div
      ref={ref}
      role="dialog"
      aria-label="Pick a reaction"
      style={style}
      className="z-50 p-1 rounded-[6px] bg-card border border-line-strong shadow-lg flex items-center justify-between gap-0.5 animate-pop-in"
    >
      {REACTION_EMOJIS.map((item) => {
        const active = reactedSet.has(item.code);
        return (
          <button
            key={item.code}
            type="button"
            title={`:${item.code}: · ${item.label}`}
            aria-label={`React with ${item.code}`}
            aria-pressed={active}
            onClick={() => {
              onPick(item.code);
              onClose();
            }}
            className={`size-7 rounded-[4px] flex items-center justify-center transition-colors ${
              active ? "bg-accent-soft shadow-[var(--ring)]" : "hover:bg-hover"
            }`}
          >
            <EmojiIcon emoji={item.code} size={17} />
          </button>
        );
      })}
    </div>,
    document.body,
  );
}

const QUICK_EMOJI_KEY = "sumi.quick_reaction";

function readQuickEmoji(): ReactionEmoji {
  try {
    const raw = localStorage.getItem(QUICK_EMOJI_KEY);
    if (raw && REACTION_EMOJIS.some((e) => e.code === raw)) return raw as ReactionEmoji;
  } catch {
    // ignore storage failures
  }
  return "thumbsup";
}

function saveQuickEmoji(code: ReactionEmoji) {
  try {
    localStorage.setItem(QUICK_EMOJI_KEY, code);
  } catch {
    // ignore storage failures
  }
}

/** One-click button for the user's most recently used reaction emoji. */
export function QuickReactionButton({
  reactions,
  onToggle,
}: {
  reactions?: ReactionGroup[];
  onToggle: (emoji: ReactionEmoji, currentlyReacted: boolean) => void;
}) {
  const [favorite, setFavorite] = useState<ReactionEmoji>(readQuickEmoji);
  useEffect(() => {
    const onSync = () => setFavorite(readQuickEmoji());
    window.addEventListener("sumi:quick-reaction", onSync);
    return () => window.removeEventListener("sumi:quick-reaction", onSync);
  }, []);
  const reacted = Boolean(reactions?.some((r) => r.emoji === favorite && r.reacted));
  const meta = getReactionEmoji(favorite);
  return (
    <button
      type="button"
      aria-label={`Quick react with ${favorite}`}
      aria-pressed={reacted}
      title={reacted ? `Remove :${favorite}:` : `React with :${favorite}: · ${meta.label}`}
      onClick={() => onToggle(favorite, reacted)}
      className={`size-7 rounded-[5px] flex items-center justify-center transition-colors ${
        reacted ? "bg-accent-soft text-accent-text" : "text-fg-2 hover:bg-hover hover:text-fg"
      }`}
    >
      <EmojiIcon emoji={favorite} size={15} />
    </button>
  );
}

/** Hover toolbar button that opens the reaction picker for a message. */
export function ReactionPickerButton({
  reactions,
  open,
  onOpenChange,
  onToggle,
  compact = false,
}: {
  reactions?: ReactionGroup[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onToggle: (emoji: ReactionEmoji, currentlyReacted: boolean) => void;
  compact?: boolean;
}) {
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const reactedSet = new Set((reactions ?? []).filter((r) => r.reacted).map((r) => r.emoji));
  return (
    <div className="relative">
      <button
        ref={triggerRef}
        type="button"
        aria-label="Add reaction"
        aria-expanded={open}
        title="Add reaction"
        onClick={() => onOpenChange(!open)}
        className={`${
          compact ? "size-7 justify-center" : "h-7 px-2 gap-1.5"
        } rounded-[5px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center transition-colors ${
          open ? "bg-hover text-fg" : ""
        }`}
      >
        <SmilePlus className="size-3.5" />
        {!compact && <span>React</span>}
      </button>
      {open && (
        <ReactionPopover
          triggerRef={triggerRef}
          reactedSet={reactedSet}
          align="right"
          onPick={(code) => {
            saveQuickEmoji(code);
            window.dispatchEvent(new Event("sumi:quick-reaction"));
            onToggle(code, reactedSet.has(code));
          }}
          onClose={() => onOpenChange(false)}
        />
      )}
    </div>
  );
}

function reactionTooltip(group: ReactionGroup, members: Member[]): string {
  const names = group.member_ids.map((id) => members.find((m) => m.id === id)?.display_name ?? "Member");
  const label = getReactionEmoji(group.emoji).label;
  if (names.length === 0) return `:${group.emoji}: · ${label}`;
  return `${names.join(", ")} reacted with :${group.emoji}:`;
}

/** Aggregated reaction chips below a message, plus a compact inline add trigger. */
export function MessageReactions({
  reactions,
  members,
  onToggle,
}: {
  reactions?: ReactionGroup[];
  members: Member[];
  onToggle: (emoji: ReactionEmoji, currentlyReacted: boolean) => void;
}) {
  const [inlineOpen, setInlineOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  if (!reactions || reactions.length === 0) return null;
  const reactedSet = new Set(reactions.filter((r) => r.reacted).map((r) => r.emoji));

  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-1">
      {reactions.map((g) => (
        <button
          key={g.emoji}
          type="button"
          title={reactionTooltip(g, members)}
          aria-label={`${g.emoji} reaction, ${g.count}`}
          aria-pressed={g.reacted}
          onClick={() => onToggle(g.emoji, g.reacted)}
          className={`h-6 pl-1.5 pr-2 rounded-[4px] border inline-flex items-center gap-1 text-[11.5px] transition-colors ${
            g.reacted
              ? "bg-accent-soft border-accent/40 text-accent-text font-medium"
              : "bg-subtle border-line text-fg-2 hover:bg-hover hover:border-line-strong"
          }`}
        >
          <EmojiIcon emoji={g.emoji} size={13} />
          <span className="tabular text-[11px] leading-none">{g.count}</span>
        </button>
      ))}
      <div className="relative">
        <button
          ref={triggerRef}
          type="button"
          aria-label="Add another reaction"
          aria-expanded={inlineOpen}
          title="Add reaction"
          onClick={() => setInlineOpen((v) => !v)}
          className="h-6 px-1.5 rounded-[5px] border border-transparent text-fg-3 hover:bg-hover hover:border-line hover:text-fg inline-flex items-center justify-center transition-colors"
        >
          <SmilePlus className="size-3" />
        </button>
        {inlineOpen && (
          <ReactionPopover
            triggerRef={triggerRef}
            reactedSet={reactedSet}
            align="left"
            onPick={(code) => onToggle(code, reactedSet.has(code))}
            onClose={() => setInlineOpen(false)}
          />
        )}
      </div>
    </div>
  );
}
