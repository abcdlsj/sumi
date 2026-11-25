import { ChevronRight, MessageSquare } from "lucide-react";
import { formatClock, type ThreadReplyPreview } from "../api";
import { MemberAvatar } from "./Avatar";

/**
 * The thread summary under a root message: the authoritative reply count plus a
 * one-line excerpt of the newest replies, so a channel row shows what the thread
 * actually says. The whole block is one button — any row opens the thread.
 */
export function ThreadReplySummary({
  replyCount = 0,
  lastReplyAt,
  replies = [],
  onOpen,
}: {
  replyCount?: number;
  lastReplyAt?: string;
  replies?: ThreadReplyPreview[];
  onOpen: () => void;
}) {
  if (replyCount <= 0) return null;
  return (
    <button
      type="button"
      onClick={onOpen}
      className="group/thread mt-2 -ml-1.5 w-full max-w-[580px] text-left rounded-[6px] border border-line border-l-2 border-l-line-strong bg-subtle/40 hover:bg-subtle hover:border-line-strong hover:border-l-accent px-2.5 py-1.5 transition-colors"
    >
      <span className="flex items-center gap-1.5 h-5 px-0.5">
        <MessageSquare className="size-3.5 text-accent-text shrink-0" />
        <span className="text-[12.5px] font-semibold text-accent-text">
          {replyCount} {replyCount === 1 ? "reply" : "replies"}
        </span>
        {lastReplyAt && <span className="ml-auto tabular text-[11.5px] text-fg-4">Last {formatClock(lastReplyAt)}</span>}
        <ChevronRight className="size-3.5 text-fg-4 opacity-0 group-hover/thread:opacity-100 transition-opacity shrink-0" />
      </span>
      {replies.map((reply) => (
        <span key={reply.message_id} className="mt-0.5 flex items-center gap-2 min-w-0 h-[22px] px-0.5">
          <MemberAvatar
            id={reply.author_member_id}
            name={reply.author_display_name ?? "?"}
            kind={reply.author_kind ?? "human"}
            size="xs"
          />
          <span className="shrink-0 max-w-[132px] truncate text-[12.5px] font-semibold text-fg-2">{reply.author_display_name}</span>
          <span className="min-w-0 flex-1 truncate text-[12.5px] text-fg-3">{reply.preview}</span>
          <span className="shrink-0 tabular text-[11px] text-fg-4">{formatClock(reply.created_at)}</span>
        </span>
      ))}
    </button>
  );
}
