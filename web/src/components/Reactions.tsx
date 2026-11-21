import { useCallback, useEffect, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { SmilePlus } from "lucide-react";
import { api, type Member, type Message, type ReactionGroup } from "../api";
import { getReactionEmoji, REACTION_EMOJIS, type ReactionEmoji } from "../emojis";
import { useAction } from "./ui";

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

/** Toggles a reaction and updates the conversation message cache immediately from the server response. */
export function useReactionToggle(
  spaceSlug: string,
  convID?: string,
  onPatched?: (messageID: string, reactions: ReactionGroup[]) => void,
) {
  const qc = useQueryClient();
  const act = useAction();

  const toggle = useCallback(
    async (messageID: string, emoji: ReactionEmoji, currentlyReacted: boolean) => {
      await act.run(async () => {
        const res = currentlyReacted
          ? await api.removeReaction(spaceSlug, messageID, emoji)
          : await api.addReaction(spaceSlug, messageID, emoji);
        const next = res.reactions ?? [];
        if (convID) {
          qc.setQueryData<{ messages: Message[]; read_seq: number; following: boolean; has_more?: boolean }>(
            ["messages", spaceSlug, convID],
            (old) =>
              old
                ? {
                    ...old,
                    messages: old.messages.map((m) => (m.id === messageID ? { ...m, reactions: next } : m)),
                  }
                : old,
          );
        }
        onPatched?.(messageID, next);
      });
    },
    [act, convID, onPatched, qc, spaceSlug],
  );

  return { toggle, busy: act.busy };
}

function ReactionPopover({
  reactedSet,
  align = "right",
  onPick,
  onClose,
}: {
  reactedSet: Set<string>;
  align?: "left" | "right";
  onPick: (emoji: ReactionEmoji) => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  return (
    <div
      ref={ref}
      role="dialog"
      aria-label="Pick a reaction"
      className={`absolute top-full mt-1.5 z-40 p-1 rounded-[10px] bg-card border border-line shadow-lg flex items-center gap-0.5 animate-pop-in ${
        align === "right" ? "right-0" : "left-0"
      }`}
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
            className={`group/emoji size-7 rounded-[7px] flex items-center justify-center transition-colors ${
              active ? "bg-accent-soft shadow-[var(--ring)]" : "hover:bg-hover"
            }`}
          >
            <EmojiIcon emoji={item.code} size={17} className="transition-transform duration-100 group-hover/emoji:scale-110" />
          </button>
        );
      })}
    </div>
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
  const reactedSet = new Set((reactions ?? []).filter((r) => r.reacted).map((r) => r.emoji));
  return (
    <div className="relative">
      <button
        type="button"
        aria-label="Add reaction"
        aria-expanded={open}
        title="Add reaction"
        onClick={() => onOpenChange(!open)}
        className={`${
          compact ? "size-7 justify-center" : "h-7 px-2 gap-1.5"
        } rounded-[7px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center transition-colors ${
          open ? "bg-hover text-fg" : ""
        }`}
      >
        <SmilePlus className="size-3.5" />
        {!compact && <span>React</span>}
      </button>
      {open && (
        <ReactionPopover
          reactedSet={reactedSet}
          align="right"
          onPick={(code) => onToggle(code, reactedSet.has(code))}
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
          className={`h-6 pl-1.5 pr-2 rounded-full border inline-flex items-center gap-1 text-[11.5px] transition-colors ${
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
          type="button"
          aria-label="Add another reaction"
          aria-expanded={inlineOpen}
          title="Add reaction"
          onClick={() => setInlineOpen((v) => !v)}
          className="h-6 px-1.5 rounded-full border border-transparent text-fg-3 hover:bg-hover hover:border-line hover:text-fg inline-flex items-center justify-center transition-colors"
        >
          <SmilePlus className="size-3" />
        </button>
        {inlineOpen && (
          <ReactionPopover
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
