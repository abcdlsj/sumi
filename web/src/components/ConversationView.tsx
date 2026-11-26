import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  AtSign,
  Bell,
  BellOff,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Copy,
  CornerUpLeft,
  FolderInput,
  Hash,
  Link2,
  ListPlus,
  Lock,
  MessageSquare,
  MoreHorizontal,
  PanelRight,
  SendHorizontal,
  Sparkles,
  Square,
  UserPlus,
  Users,
  X,
} from "lucide-react";
import { api, formatClock, formatDay, type AgentCreateProposal, type BootstrapData, type Conversation, type Member, type Message, type NotifyLevel } from "../api";
import { readDraft, writeDraft } from "../drafts";
import { isComposerSendEvent, isMacPlatform, useSendKey } from "../preferences";
import { ActionCard, isActionCardMessage } from "./ActionCard";
import { memberDetailTarget, type DetailTarget } from "./DetailPane";
import { AgentReadReceipts, buildAgentReadMap, MemberHoverCard, WorkingAgentsPopover } from "./AgentVisibility";
import { agentFocus, agentStatusText, MemberAvatar, StatusDot } from "./Avatar";
import { AttachmentDraftRow, AttachmentPicker, MessageAttachments, useAttachmentDraft } from "./Attachments";
import { CollapsibleMessage } from "./CollapsibleMessage";
import { MentionPopup, useMentionMembers, useMentionPicker } from "./MentionPicker";
import { MessageBody } from "./MessageBody";
import { MessageReactions, QuickReactionButton, ReactionPickerButton, useQuickReactionEmoji, useReactionToggle } from "./Reactions";
import type { ReactionEmoji } from "../emojis";
import { ThreadReplySummary } from "./ThreadReplies";
import { LoadEarlierButton, mergeMessageIntoCache, NewMessagesPill, useOlderMessages, useStickToBottom } from "./scroll";
import { MessageListSkeleton } from "./Skeletons";
import { Badge, Button, EmptyState, ErrorState, IconButton, Menu, MenuItem, Modal, TaskStatusIcon, useAction, useToast } from "./ui";

const GROUP_WINDOW_MS = 5 * 60 * 1000;
const NOTIFY: { value: NotifyLevel; label: string; hint: string; icon: React.ReactNode }[] = [
  { value: "mentions", label: "Mentions only", hint: "Notify on @mentions and followed threads", icon: <AtSign /> },
  { value: "all", label: "All messages", hint: "Every new message counts as unread", icon: <Bell /> },
  { value: "mute", label: "Muted", hint: "Dim in sidebar; @mentions still reach you", icon: <BellOff /> },
];

function sameDay(a: string, b: string) {
  return new Date(a).toDateString() === new Date(b).toDateString();
}

function findMentionedOutsideMembers(text: string, spaceMembers: Member[], channelMembers: Member[], selfID: string): Member[] {
  if (!text.includes("@")) return [];
  const inChannelIDs = new Set(channelMembers.map((m) => m.id));
  const byName = new Map<string, Member>();
  for (const m of spaceMembers) {
    if (m.id !== selfID && !inChannelIDs.has(m.id)) {
      byName.set(m.display_name.toLowerCase(), m);
    }
  }
  if (byName.size === 0) return [];
  const re = /(?:^|[^A-Za-z0-9_])@([\p{L}\p{N}_]+)/gu;
  const found = new Map<string, Member>();
  let match = re.exec(text);
  while (match) {
    const m = byName.get(match[1].toLowerCase());
    if (m) found.set(m.id, m);
    match = re.exec(text);
  }
  return Array.from(found.values());
}

/** IDs present on first render are not animated; later arrivals rise in. */
function useArrivals(convID: string, messages: Message[] | undefined) {
  const seen = useRef<{ conv: string; ids: Set<string> | null }>({ conv: "", ids: null });
  if (seen.current.conv !== convID) seen.current = { conv: convID, ids: null };
  if (messages && seen.current.ids === null) seen.current.ids = new Set(messages.map((m) => m.id));
  const initial = seen.current.ids;
  return (id: string) => Boolean(initial && !initial.has(id));
}

export function describeAction(kind?: string, raw?: string): string | undefined {
  try {
    const p = raw ? JSON.parse(raw) : {};
    if (kind === "agent_created") return `created agent ${p.display_name ?? ""}`;
    if (kind === "channel_created") return `created #${p.slug ?? ""}`;
    if (kind === "channel_archived") return `archived #${p.slug ?? ""}`;
    if (kind === "channel_unarchived") return `unarchived #${p.slug ?? ""}`;
    if (kind === "task_event") {
      if (p.op === "created") return `created #${p.task_number}`;
      if (p.op === "claimed") return `claimed #${p.task_number}`;
      if (p.op === "unclaimed") return `unclaimed #${p.task_number}`;
      if (p.op === "assigned") return `assigned #${p.task_number} to ${p.assignee_name ?? "nobody"}${p.reason ? ` — ${p.reason}` : ""}`;
      if (p.op === "status_changed") return `moved #${p.task_number} from ${p.from} to ${p.to}`;
      if (p.op === "amended") return `edited #${p.task_number} (rev ${p.revision})`;
    }
  } catch {
    // ignore malformed payload
  }
  return kind;
}

/** Fixed-height runtime line above the composer so agent state transitions stay visible without shifting layout. */
function AgentStatusLine({
  spaceSlug,
  boot,
  conv,
  onOpenDetail,
  onOpenConversation,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  conv: Conversation;
  onOpenDetail: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const [popOpen, setPopOpen] = useState(false);
  const [stoppingID, setStoppingID] = useState<string | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const canGovern = boot.current_member.access_level !== "member";
  const agents = boot.members.filter((m) => m.kind === "agent");
  const peer = conv.kind === "dm" ? agents.find((a) => a.id === conv.peer_member_id) : undefined;
  const busyHere = agents.filter((a) => a.agent?.runtime_state === "working" && a.agent.focus_conversation_id === conv.id);

  async function stopAgent(agentID: string) {
    setStoppingID(agentID);
    await act.run(() => api.controlAgent(spaceSlug, agentID, "stop_turn"));
    setStoppingID(null);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  let statusContent: React.ReactNode = null;
  if (busyHere.length > 0 && conv.kind !== "dm") {
    const names = busyHere.map((a) => a.display_name);
    const label =
      names.length === 1
        ? `${names[0]} is working on a reply…`
        : names.length === 2
          ? `${names[0]} and ${names[1]} are working here`
          : `${names[0]}, ${names[1]} and ${names.length - 2} ${names.length - 2 === 1 ? "other" : "others"} are working here`;
    statusContent = (
      <>
        <StatusDot state="working" ring="var(--panel)" size={7} />
        <button
          ref={triggerRef}
          type="button"
          onClick={() => (busyHere.length === 1 ? onOpenDetail({ kind: "agent", memberID: busyHere[0].id }) : setPopOpen((v) => !v))}
          className="min-w-0 truncate text-busy hover:underline underline-offset-2"
        >
          {busyHere.length === 1 ? (
            <>
              <span className="font-semibold">{names[0]}</span> is working on a reply…
            </>
          ) : (
            <span className="font-medium">{label}</span>
          )}
        </button>
        {canGovern && busyHere.length === 1 && (
          <button
            type="button"
            disabled={stoppingID === busyHere[0].id}
            onClick={() => void stopAgent(busyHere[0].id)}
            aria-label={`Stop ${busyHere[0].display_name}`}
            className="inline-flex items-center gap-1 px-1.5 h-5 rounded-[4px] text-[11.5px] font-medium text-fg-3 hover:text-danger hover:bg-hover disabled:opacity-50 shrink-0 transition-colors"
          >
            <Square className="size-2.5 fill-current" />
            Stop
          </button>
        )}
        {busyHere.length > 1 && (
          <WorkingAgentsPopover
            boot={boot}
            agents={busyHere}
            open={popOpen}
            triggerRef={triggerRef}
            align="left"
            onClose={() => setPopOpen(false)}
            onOpenConversation={onOpenConversation}
            onOpenDetail={onOpenDetail}
            onStopAgent={canGovern ? (id) => void stopAgent(id) : undefined}
          />
        )}
      </>
    );
  } else {
    const subject: Member | undefined = peer ?? busyHere[0];
    if (subject?.agent) {
      const st = subject.agent.runtime_state;
      const focus = agentFocus(boot, subject);
      const elsewhere = st === "working" && subject.agent.focus_conversation_id !== conv.id;
      const tone = st === "error" ? "text-danger" : st === "working" ? "text-busy" : "text-fg-3";
      let text = agentStatusText(boot, subject);
      if (st === "working" && !elsewhere) text = "is working on a reply…";
      else if (elsewhere) text = `is busy in ${focus?.label ?? "another conversation"} · your message is queued`;
      else if (st === "idle") text = "is idle · messages wake it";
      else if (st === "offline") text = "is offline · messages wait until its computer reconnects";
      else if (st === "error") text = `hit an error · ${subject.agent.last_error_code || "see details"}`;

      statusContent = (
        <>
          <StatusDot state={st} ring="var(--panel)" size={7} />
          <button onClick={() => onOpenDetail({ kind: "agent", memberID: subject.id })} className={`min-w-0 truncate hover:underline underline-offset-2 ${tone}`}>
            <span className="font-semibold">{subject.display_name}</span> {text}
          </button>
          {canGovern && st === "working" && (
            <button
              type="button"
              disabled={stoppingID === subject.id}
              onClick={() => void stopAgent(subject.id)}
              aria-label={`Stop ${subject.display_name}`}
              className="inline-flex items-center gap-1 px-1.5 h-5 rounded-[4px] text-[11.5px] font-medium text-fg-3 hover:text-danger hover:bg-hover disabled:opacity-50 shrink-0 transition-colors"
            >
              <Square className="size-2.5 fill-current" />
              Stop
            </button>
          )}
        </>
      );
    }
  }

  return (
    <div className="h-6 px-5 pt-1 flex items-center gap-1.5 text-[12px] shrink-0" aria-live="polite">
      {statusContent}
    </div>
  );
}

function ActionMessageGroup({ items }: { items: Message[] }) {
  const [open, setOpen] = useState(false);
  const authors = Array.from(new Set(items.map((m) => m.author_display_name).filter(Boolean)));
  const summaryAuthors = authors.slice(0, 3).join(", ") + (authors.length > 3 ? ` +${authors.length - 3}` : "");
  const lastAt = items[items.length - 1]?.created_at;
  return (
    <div className="px-6 py-1">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex items-center gap-3 text-[12.5px] text-fg-3 hover:text-fg transition-colors py-0.5"
      >
        <span className="w-9 flex justify-center shrink-0">
          <span className="size-5 rounded-[4px] bg-subtle border border-line flex items-center justify-center">
            {open ? <ChevronDown className="size-3 text-fg-3" /> : <ChevronRight className="size-3 text-fg-3" />}
          </span>
        </span>
        <span className="font-medium text-fg-2">{items.length} updates</span>
        {summaryAuthors && <span className="text-fg-4 truncate">· {summaryAuthors}</span>}
        {lastAt && <span className="tabular text-[11.5px] text-fg-4 shrink-0">{formatClock(lastAt)}</span>}
      </button>
      {open && (
        <div className="mt-1 ml-12 pl-3 border-l border-line space-y-1 animate-fade-in">
          {items.map((m) => (
            <div key={m.id} className="text-[12.5px] text-fg-3 leading-relaxed">
              <span className="font-medium text-fg-2">{m.author_display_name}</span>{" "}
              {describeAction(m.action_kind, m.payload_json) ?? m.body_markdown}
              <span className="ml-2 tabular text-[11.5px] text-fg-4">{formatClock(m.created_at)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

type StreamEntry =
  | { kind: "message"; msg: Message; idx: number }
  | { kind: "action_group"; key: string; items: Message[]; firstIdx: number };

function buildStreamEntries(messages: Message[], boundary: number, selfID: string): StreamEntry[] {
  const entries: StreamEntry[] = [];
  let i = 0;
  while (i < messages.length) {
    const m = messages[i];
    if (m.kind === "text" || isActionCardMessage(m)) {
      entries.push({ kind: "message", msg: m, idx: i });
      i++;
      continue;
    }
    const run: Message[] = [m];
    let j = i + 1;
    while (j < messages.length) {
      const next = messages[j];
      if (next.kind === "text" || isActionCardMessage(next)) break;
      if (!sameDay(run[run.length - 1].created_at, next.created_at)) break;
      const crossesNew = boundary > 0 && next.seq > boundary && next.author_member_id !== selfID && run[run.length - 1].seq <= boundary;
      if (crossesNew) break;
      run.push(next);
      j++;
    }
    if (run.length >= 2) {
      entries.push({ kind: "action_group", key: `ag:${run[0].id}:${run.length}`, items: run, firstIdx: i });
    } else {
      entries.push({ kind: "message", msg: m, idx: i });
    }
    i = j;
  }
  return entries;
}

function DayDivider({ iso }: { iso: string }) {
  return (
    <div className="sticky top-0 z-10 px-6 py-2.5 flex items-center gap-3 pointer-events-none">
      <span className="flex-1 h-px bg-line" />
      <span className="h-6 px-2.5 rounded-[4px] bg-panel border border-line-strong text-[11.5px] font-semibold text-fg-2 inline-flex items-center">
        {formatDay(iso)}
      </span>
      <span className="flex-1 h-px bg-line" />
    </div>
  );
}

function UnreadDivider() {
  return (
    <div data-unread-anchor="new" className="px-6 py-2 flex items-center gap-2.5 animate-fade-in">
      <div className="flex-1 h-px bg-accent/70" />
      <span className="h-[18px] px-1.5 rounded-[3px] bg-accent text-accent-fg text-[10.5px] font-semibold uppercase tracking-[0.05em] inline-flex items-center">
        New messages
      </span>
      <div className="flex-1 h-px bg-accent/70" />
    </div>
  );
}

/** Isolated composer so typing never re-renders the message list above it. */
function MessageComposer({
  spaceSlug,
  boot,
  conv,
  title,
  replyingTo,
  replyTarget,
  onCancelReply,
  onSent,
  focusRef,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  conv: Conversation;
  title: string;
  replyingTo: string | null;
  replyTarget?: Message;
  onCancelReply: () => void;
  onSent: () => void;
  focusRef: React.MutableRefObject<(() => void) | null>;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [composer, setComposer] = useState(() => readDraft(conv.id)?.text ?? "");
  const [sendKey] = useSendKey();
  const draft = useAttachmentDraft(spaceSlug);
  const send = useAction();
  const inviteAct = useAction();
  const textRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    focusRef.current = () => textRef.current?.focus();
    textRef.current?.focus();
    return () => {
      focusRef.current = null;
    };
  }, [conv.id, focusRef]);

  useEffect(() => {
    writeDraft(conv.id, { text: composer, replyTo: replyingTo });
  }, [conv.id, composer, replyingTo]);

  useLayoutEffect(() => {
    const el = textRef.current;
    if (!el) return;
    const prevScroll = el.scrollTop;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 220)}px`;
    if (el.scrollHeight > 220) el.scrollTop = prevScroll;
  }, [composer]);

  const mentionMembers = useMentionMembers(spaceSlug, boot, conv.id, conv.kind === "dm" ? conv.peer_member_id : undefined);
  const isChannel = conv.kind === "public" || conv.kind === "private";
  const canManageMembers =
    boot.current_member.access_level !== "member" || (boot.current_member.permissions ?? []).includes("channel.manage_members");
  const outsideMentions = useMemo(
    () =>
      isChannel && mentionMembers.length > 0
        ? findMentionedOutsideMembers(composer, boot.members, mentionMembers, boot.current_member.id)
        : [],
    [isChannel, composer, boot.members, mentionMembers, boot.current_member.id],
  );
  const mentions = useMentionPicker({
    members: mentionMembers,
    extraMembers: isChannel ? boot.members : [],
    selfID: boot.current_member.id,
    value: composer,
    onChange: setComposer,
    inputRef: textRef,
  });

  async function handleSend(e?: FormEvent) {
    e?.preventDefault();
    const body = composer.trim();
    if ((!body && draft.ids.length === 0) || draft.uploading || send.busy) return;
    let sentMsg: Message | null = null;
    const ok = await send.run(async () => {
      const res = await api.sendMessage(
        spaceSlug,
        conv.id,
        body || `Attached ${draft.pending.map((a) => a.name).join(", ")}`,
        replyingTo ?? undefined,
        draft.ids,
      );
      sentMsg = res.message;
    });
    if (!ok) return;
    setComposer("");
    onCancelReply();
    draft.clear();
    mentions.close();
    if (sentMsg && !replyingTo && (sentMsg as Message).conversation_id === conv.id) {
      mergeMessageIntoCache(qc, spaceSlug, conv.id, sentMsg);
    }
    onSent();
    textRef.current?.focus();
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  function insertMentionTrigger() {
    const el = textRef.current;
    const start = el?.selectionStart ?? composer.length;
    const end = el?.selectionEnd ?? composer.length;
    const before = composer.slice(0, start);
    const needsSpace = before.length > 0 && !/[\s(]$/.test(before);
    const insert = needsSpace ? " @" : "@";
    const next = before + insert + composer.slice(end);
    const caret = before.length + insert.length;
    setComposer(next);
    window.requestAnimationFrame(() => {
      const box = textRef.current;
      if (!box) return;
      box.focus();
      box.setSelectionRange(caret, caret);
      mentions.detect();
    });
  }

  return (
    <form onSubmit={(e) => void handleSend(e)} className="px-5 pt-1 pb-3.5">
      <div
        {...draft.dropProps}
        className={`relative bg-card border rounded-[6px] focus-within:border-accent focus-within:shadow-[var(--ring)] transition-[border-color,box-shadow,background-color] ${
          draft.dragging ? "border-accent bg-accent-soft/40" : "border-line-strong"
        }`}
      >
        <MentionPopup picker={mentions} />
        {outsideMentions.length > 0 && (
          <div className="mx-2 mt-2 px-3 py-2 rounded-[4px] bg-busy-soft/60 border border-busy/25 flex items-center gap-2 text-[12.5px] text-fg-2 animate-fade-in">
            <UserPlus className="size-3.5 text-busy shrink-0" />
            <span className="min-w-0 flex-1 truncate">
              <span className="font-semibold text-fg">{outsideMentions.map((m) => `@${m.display_name}`).join(", ")}</span>{" "}
              {outsideMentions.length === 1 ? "isn't" : "aren't"} in #{title} and won&apos;t see or wake from this message.
            </span>
            {canManageMembers && (
              <Button
                type="button"
                size="sm"
                variant="secondary"
                loading={inviteAct.busy}
                onClick={() =>
                  void inviteAct.run(async () => {
                    for (const m of outsideMentions) {
                      await api.inviteToChannel(spaceSlug, conv.id, m.id);
                    }
                    toast(`Added ${outsideMentions.map((m) => m.display_name).join(", ")} to #${title}`, "success");
                    await qc.invalidateQueries({ queryKey: ["conv-members", spaceSlug, conv.id] });
                    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                  })
                }
              >
                Invite
              </Button>
            )}
          </div>
        )}
        {replyTarget && (
          <div className="mx-2 mt-2 px-3 py-1.5 rounded-[4px] bg-subtle border border-line flex items-center gap-2 text-[13px] animate-fade-in">
            <CornerUpLeft className="size-3.5 text-fg-3 shrink-0" />
            <span className="text-fg-3 shrink-0">Replying in thread to</span>
            <span className="font-semibold text-fg shrink-0">{replyTarget.author_display_name}</span>
            <span className="truncate text-fg-3">{replyTarget.body_markdown.slice(0, 140)}</span>
            <span className="flex-1" />
            <button type="button" onClick={onCancelReply} aria-label="Cancel thread reply" className="size-6 rounded-[4px] hover:bg-hover text-fg-3 flex items-center justify-center shrink-0">
              <X className="size-3.5" />
            </button>
          </div>
        )}
        <textarea
          ref={textRef}
          rows={1}
          value={composer}
          onChange={(e) => {
            setComposer(e.target.value);
            mentions.detect();
          }}
          onClick={mentions.detect}
          onPaste={draft.handlePaste}
          onBlur={mentions.close}
          onKeyDown={(e) => {
            if (mentions.handleKeyDown(e)) return;
            if (isComposerSendEvent(e, sendKey)) {
              e.preventDefault();
              void handleSend();
            }
            if (e.key === "Escape" && replyingTo) onCancelReply();
          }}
          placeholder={replyingTo ? "Reply in thread…" : `Message ${conv.kind === "dm" ? title : "#" + title}`}
          aria-label={replyingTo ? "Reply in thread" : `Message ${conv.kind === "dm" ? title : "#" + title}`}
          className="block w-full px-3.5 pt-2.5 pb-2 min-h-[44px] text-[length:var(--msg-font-size)] leading-relaxed bg-transparent outline-none resize-none placeholder:text-fg-4 scroll-thin"
        />
        <AttachmentDraftRow draft={draft} />
        <div className="px-2 py-1.5 border-t border-line bg-subtle/45 rounded-b-[5px] flex items-center justify-between gap-2">
          <div className="flex items-center gap-0.5">
            <AttachmentPicker draft={draft} />
            <button
              type="button"
              onMouseDown={(e) => e.preventDefault()}
              onClick={insertMentionTrigger}
              title="Mention someone (@)"
              aria-label="Mention someone"
              className="size-7 rounded-[4px] hover:bg-hover text-fg-3 hover:text-fg flex items-center justify-center transition-colors"
            >
              <AtSign className="size-4" />
            </button>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-[11.5px] text-fg-4 hidden md:inline">
              {sendKey === "mod_enter" ? (
                <>
                  <kbd className="font-sans">{isMacPlatform() ? "⌘" : "Ctrl"}+Enter</kbd> send · <kbd className="font-sans">Enter</kbd> newline
                </>
              ) : (
                <>
                  <kbd className="font-sans">Enter</kbd> send · <kbd className="font-sans">Shift+Enter</kbd> newline
                </>
              )}
            </span>
            <button
              type="submit"
              aria-label="Send"
              disabled={(!composer.trim() && draft.ids.length === 0) || draft.uploading || send.busy}
              className="h-7 px-2.5 rounded-[4px] bg-accent text-accent-fg hover:bg-accent-hover active:brightness-95 disabled:bg-subtle disabled:text-fg-4 flex items-center justify-center gap-1.5 text-[12.5px] font-medium transition-colors"
            >
              <span>Send</span>
              <SendHorizontal className="size-3.5" />
            </button>
          </div>
        </div>
      </div>
    </form>
  );
}

/** Memoized single message row so scroll-state flips and single-row menus skip reconciling unchanged rows. */
const MessageRow = memo(function MessageRow({
  spaceSlug,
  convSlug,
  boot,
  m,
  author,
  grouped,
  animateArrival,
  showDay,
  showNew,
  highlighted,
  isArchived,
  canGovern,
  creatingTask,
  newTaskTitle,
  taskBusy,
  reactionPickerOpen,
  moreMenuOpen,
  quickEmoji,
  readAgents,
  onOpenDetail,
  onOpenDM,
  onReviewAgentProposal,
  onReply,
  onStartTask,
  onChangeTaskTitle,
  onCancelTask,
  onSubmitTask,
  onToggleReaction,
  onReactionPickerChange,
  onMoreMenuChange,
  onCopyText,
  onCopyLink,
}: {
  spaceSlug: string;
  convSlug: string;
  boot: BootstrapData;
  m: Message;
  author?: Member;
  grouped: boolean;
  animateArrival: boolean;
  showDay: boolean;
  showNew: boolean;
  highlighted: boolean;
  isArchived: boolean;
  canGovern: boolean;
  creatingTask: boolean;
  newTaskTitle: string;
  taskBusy: boolean;
  reactionPickerOpen: boolean;
  moreMenuOpen: boolean;
  quickEmoji: ReactionEmoji;
  readAgents?: Member[];
  onOpenDetail: (t: DetailTarget) => void;
  onOpenDM?: (memberID: string) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
  onReply: (messageID: string) => void;
  onStartTask: (messageID: string) => void;
  onChangeTaskTitle: (title: string) => void;
  onCancelTask: () => void;
  onSubmitTask: (e: FormEvent, sourceMsgID: string) => void;
  onToggleReaction: (messageID: string, emoji: ReactionEmoji, reacted: boolean) => void;
  onReactionPickerChange: (messageID: string, open: boolean) => void;
  onMoreMenuChange: (messageID: string, open: boolean) => void;
  onCopyText: (body: string) => void;
  onCopyLink: (messageID: string) => void;
}) {
  const toolbarPinned = reactionPickerOpen || moreMenuOpen;
  return (
    <div data-message-id={m.id} className={animateArrival ? "animate-rise-in" : undefined}>
      {showDay && <DayDivider iso={m.created_at} />}
      {showNew && <UnreadDivider />}
      {isActionCardMessage(m) ? (
        <article className="msg-row px-6 pt-3 pb-1.5 flex items-start gap-3">
          <div className="w-9 shrink-0">
            <button
              type="button"
              onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
              className="block mt-0.5"
              aria-label={m.author_kind === "agent" ? `Inspect ${m.author_display_name}` : `View ${m.author_display_name}`}
            >
              <MemberAvatar
                id={m.author_member_id}
                name={m.author_display_name ?? "?"}
                kind={m.author_kind ?? "human"}
                size="md"
                state={author?.agent?.runtime_state}
              />
            </button>
          </div>
          <div className="min-w-0 flex-1 max-w-[920px]">
            <div className="flex items-baseline gap-2 h-5">
              <button
                type="button"
                onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
                className="text-[14px] font-bold text-fg hover:underline underline-offset-2"
              >
                {m.author_display_name}
              </button>
              <span className="tabular text-[11.5px] text-fg-4">{formatClock(m.created_at)}</span>
            </div>
            <ActionCard
              spaceSlug={spaceSlug}
              message={m}
              canGovern={canGovern}
              onReviewAgentProposal={onReviewAgentProposal}
              onInspectAgent={(memberID) => onOpenDetail({ kind: "agent", memberID })}
            />
          </div>
        </article>
      ) : m.kind !== "text" ? (
        <div className="msg-row px-6 py-1.5 flex items-center gap-3 text-[13px] text-fg-3">
          <span className="w-9 flex justify-center shrink-0">
            <span className="size-5 rounded-[4px] bg-subtle border border-line flex items-center justify-center">
              <Sparkles className="size-3 text-fg-3" />
            </span>
          </span>
          <span className="min-w-0">
            <span className="font-medium text-fg-2">{m.author_display_name}</span> {describeAction(m.action_kind, m.payload_json) ?? m.body_markdown}
            <span className="ml-2 tabular text-[11.5px] text-fg-4">{formatClock(m.created_at)}</span>
          </span>
        </div>
      ) : (
        <article
          className={`msg-row relative px-6 ${grouped ? "py-[3px]" : "pt-3 pb-1"} flex items-start gap-3 group ${
            highlighted ? "bg-accent-soft" : "hover:bg-subtle/85"
          }`}
        >
          <div className="w-9 shrink-0">
            {grouped ? (
              <span className="block pt-[3px] text-right tabular text-[11px] leading-5 text-fg-4 opacity-0 group-hover:opacity-100">
                {formatClock(m.created_at)}
              </span>
            ) : (
              <button
                onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
                className="block mt-0.5"
                aria-label={m.author_kind === "agent" ? `Inspect ${m.author_display_name}` : `View ${m.author_display_name}`}
              >
                <MemberAvatar
                  id={m.author_member_id}
                  name={m.author_display_name ?? "?"}
                  kind={m.author_kind ?? "human"}
                  size="md"
                  state={author?.agent?.runtime_state}
                />
              </button>
            )}
          </div>
          <div className="min-w-0 flex-1 max-w-[920px]">
            {!grouped && (
              <div className="flex items-baseline gap-2 h-5">
                <MemberHoverCard
                  boot={boot}
                  member={author}
                  onInspect={(member) => onOpenDetail(memberDetailTarget(member))}
                  onMessage={onOpenDM}
                >
                  <button
                    type="button"
                    onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
                    className="text-[14px] font-bold text-fg hover:underline underline-offset-2"
                  >
                    {m.author_display_name}
                  </button>
                </MemberHoverCard>
                <span className="tabular text-[11.5px] text-fg-4">{formatClock(m.created_at)}</span>
              </div>
            )}
            <div className={`${grouped ? "" : "mt-0.5"} text-[length:var(--msg-font-size)] leading-[var(--msg-line-height)] text-fg md`}>
              <CollapsibleMessage messageID={m.id}>
                <MessageBody
                  body={m.body_markdown}
                  members={boot.members}
                  tasks={boot.tasks}
                  boot={boot}
                  selfID={boot.current_member.id}
                  onSelectMember={(member) => onOpenDetail(memberDetailTarget(member))}
                  onSelectTask={(tk) => onOpenDetail({ kind: "task", taskID: tk.id })}
                  onOpenWorkspaceFile={
                    m.author_kind === "agent"
                      ? (filePath) =>
                          onOpenDetail({
                            kind: "agent",
                            memberID: m.author_member_id,
                            tab: "workspace",
                            filePath,
                            openNonce: Date.now(),
                          })
                      : undefined
                  }
                />
              </CollapsibleMessage>
            </div>

            <MessageAttachments spaceSlug={spaceSlug} attachments={m.attachments} />

            <MessageReactions
              reactions={m.reactions}
              members={boot.members}
              onToggle={(emoji, reacted) => onToggleReaction(m.id, emoji, reacted)}
            />

            {m.attached_tasks && m.attached_tasks.length > 0 && (
              <div className="mt-2 rounded-[6px] border border-line-strong bg-card divide-y divide-line overflow-hidden max-w-[580px]">
                {m.attached_tasks.map((tk) => (
                  <button
                    key={tk.id}
                    onClick={() => onOpenDetail({ kind: "task", taskID: tk.id })}
                    className="w-full h-10 px-3 hover:bg-subtle flex items-center gap-2.5 text-left"
                  >
                    <TaskStatusIcon status={tk.status} />
                    <span className="tabular text-[13px] text-fg-3 shrink-0">#{tk.number}</span>
                    <span className="text-[14px] font-medium text-fg truncate flex-1">{tk.title}</span>
                    {tk.assignee_member_id ? (
                      <span className="flex items-center gap-1.5 text-[13px] text-fg-2 shrink-0">
                        <MemberAvatar id={tk.assignee_member_id} name={tk.assignee_display_name ?? "?"} kind={tk.assignee_kind ?? "human"} size="xs" />
                        {tk.assignee_display_name}
                      </span>
                    ) : (
                      <span className="text-[13px] text-fg-4 shrink-0">Unassigned</span>
                    )}
                  </button>
                ))}
              </div>
            )}

            {creatingTask && (
              <form onSubmit={(e) => onSubmitTask(e, m.id)} className="mt-2.5 flex items-center gap-2 max-w-[560px] animate-rise-in">
                <input
                  autoFocus
                  value={newTaskTitle}
                  onChange={(e) => onChangeTaskTitle(e.target.value)}
                  onKeyDown={(e) => e.key === "Escape" && onCancelTask()}
                  placeholder="Task title"
                  className="flex-1 h-9 px-3 rounded-[6px] border border-accent bg-card text-[14px] outline-none shadow-[var(--ring)]"
                />
                <Button type="submit" size="sm" loading={taskBusy} disabled={!newTaskTitle.trim()}>
                  Create task
                </Button>
                <Button type="button" size="sm" variant="ghost" onClick={onCancelTask}>
                  Cancel
                </Button>
              </form>
            )}

            {m.reply_count && m.reply_count > 0 && m.thread_conversation_id ? (
              <ThreadReplySummary
                replyCount={m.reply_count}
                lastReplyAt={m.last_reply_at}
                replies={m.latest_replies}
                onOpen={() =>
                  onOpenDetail({ kind: "thread", threadConvID: m.thread_conversation_id!, rootMessageID: m.id, parentSlug: convSlug })
                }
              />
            ) : null}

            <AgentReadReceipts
              agents={readAgents}
              boot={boot}
              onSelectAgent={(memberID) => onOpenDetail({ kind: "agent", memberID })}
            />
          </div>

          {!isArchived && (
            <div
              className={`absolute right-5 -top-3.5 ${
                toolbarPinned
                  ? "opacity-100 pointer-events-auto"
                  : "opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus-within:opacity-100 focus-within:pointer-events-auto"
              } flex items-center gap-0.5 bg-card border border-line-strong rounded-[5px] p-0.5 shadow-md z-20`}
            >
              <QuickReactionButton
                reactions={m.reactions}
                favorite={quickEmoji}
                onToggle={(emoji, reacted) => onToggleReaction(m.id, emoji, reacted)}
              />
              <ReactionPickerButton
                reactions={m.reactions}
                open={reactionPickerOpen}
                onOpenChange={(open) => onReactionPickerChange(m.id, open)}
                onToggle={(emoji, reacted) => onToggleReaction(m.id, emoji, reacted)}
              />
              <button
                onClick={() => onReply(m.id)}
                className="h-7 px-2 rounded-[4px] text-[12.5px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
              >
                <CornerUpLeft className="size-3.5" /> Reply
              </button>
              <button
                onClick={() => onStartTask(m.id)}
                className="h-7 px-2 rounded-[4px] text-[12.5px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
              >
                <ListPlus className="size-3.5" /> Task
              </button>
              <Menu
                width={160}
                onOpenChange={(open) => onMoreMenuChange(m.id, open)}
                trigger={(open, toggle) => (
                  <button
                    type="button"
                    aria-label="More message actions"
                    aria-expanded={open}
                    onClick={toggle}
                    className={`size-7 rounded-[4px] text-fg-2 hover:bg-hover hover:text-fg flex items-center justify-center ${
                      open ? "bg-hover text-fg" : ""
                    }`}
                  >
                    <MoreHorizontal className="size-3.5" />
                  </button>
                )}
              >
                {(close) => (
                  <>
                    <MenuItem
                      icon={<Copy />}
                      onSelect={() => {
                        close();
                        onCopyText(m.body_markdown);
                      }}
                    >
                      Copy text
                    </MenuItem>
                    <MenuItem
                      icon={<Link2 />}
                      onSelect={() => {
                        close();
                        onCopyLink(m.id);
                      }}
                    >
                      Copy link
                    </MenuItem>
                  </>
                )}
              </Menu>
            </div>
          )}
        </article>
      )}
    </div>
  );
});

export function ConversationView({
  spaceSlug,
  boot,
  conv,
  focusMessageID,
  activeDetail,
  onOpenDetail,
  onOpenConversation,
  onOpenDM,
  onReviewAgentProposal,
  onArchived,
  onUnarchived,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  conv: Conversation;
  focusMessageID?: string;
  activeDetail?: DetailTarget | null;
  onOpenDetail: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
  onOpenDM?: (memberID: string) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
  onArchived?: (conv: Conversation) => void;
  onUnarchived?: (conv: Conversation) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [replyingTo, setReplyingTo] = useState<string | null>(() => readDraft(conv.id)?.replyTo ?? null);
  const [creatingTaskOnMsg, setCreatingTaskOnMsg] = useState<string | null>(null);
  const [newTaskTitle, setNewTaskTitle] = useState("");
  const [reactionPickerMsgID, setReactionPickerMsgID] = useState<string | null>(null);
  const [moreMenuMsgID, setMoreMenuMsgID] = useState<string | null>(null);
  const [highlightedMsgID, setHighlightedMsgID] = useState<string | null>(null);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const taskAction = useAction();
  const notify = useAction();
  const archiveAct = useAction();
  const composerFocusRef = useRef<(() => void) | null>(null);
  const isArchived = Boolean(conv.archived_at);
  const canGovern = boot.current_member.access_level !== "member";
  const isChannel = conv.kind === "public" || conv.kind === "private";

  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, conv.id],
    queryFn: () => api.listMessages(spaceSlug, conv.id, true),
  });
  const tailMessages = msgsQ.data?.messages;
  const isNew = useArrivals(conv.id, tailMessages);
  const lastTailMsg = tailMessages && tailMessages.length > 0 ? tailMessages[tailMessages.length - 1] : undefined;
  const { ref: scrollRef, onScroll, newCount, farFromEnd, jumpToEnd, syncScrollTop } = useStickToBottom({
    streamKey: conv.id,
    count: tailMessages?.length ?? 0,
    lastSeq: lastTailMsg?.seq,
    lastId: lastTailMsg?.id,
    anchorId: "new",
  });
  const history = useOlderMessages({
    spaceSlug,
    convID: conv.id,
    latest: tailMessages,
    latestHasMore: msgsQ.data?.has_more,
    scrollRef,
    onAdjustScroll: syncScrollTop,
  });
  const messages = history.messages;
  const agentReadMap = useMemo(
    () => buildAgentReadMap(messages, msgsQ.data?.agent_reads, boot.members),
    [messages, msgsQ.data?.agent_reads, boot.members],
  );
  const reactions = useReactionToggle(
    spaceSlug,
    conv.id,
    (messageID, next) => {
      history.patchMessage(messageID, (m) => ({ ...m, reactions: next }));
    },
    boot.current_member.id,
  );

  useEffect(() => {
    setCreatingTaskOnMsg(null);
    setReactionPickerMsgID(null);
  }, [conv.id]);

  useEffect(() => {
    if (!focusMessageID || !messages) return;
    const el = scrollRef.current?.querySelector<HTMLElement>(`[data-message-id="${focusMessageID}"]`);
    if (!el) return;
    el.scrollIntoView?.({ block: "center", behavior: "smooth" });
    setHighlightedMsgID(focusMessageID);
    const timer = window.setTimeout(() => setHighlightedMsgID((cur) => (cur === focusMessageID ? null : cur)), 2000);
    return () => window.clearTimeout(timer);
  }, [focusMessageID, messages, scrollRef]);

  const peer = conv.kind === "dm" ? boot.members.find((m) => m.id === conv.peer_member_id) : undefined;
  const title = conv.kind === "dm" ? conv.peer_display_name ?? "Direct message" : conv.slug ?? "";
  const replyTarget = useMemo(() => messages?.find((m) => m.id === replyingTo), [messages, replyingTo]);
  const level = conv.notify_level ?? "mentions";
  const readSeq = msgsQ.data?.read_seq ?? 0;
  // The unread boundary of this visit: later fetches advance the watermark, the divider stays put.
  const settled = msgsQ.isFetchedAfterMount || msgsQ.isError;
  const [unreadBase, setUnreadBase] = useState<number | null>(null);
  const boundary = unreadBase ?? readSeq;

  useEffect(() => {
    setUnreadBase(null);
  }, [conv.id]);
  useEffect(() => {
    if (settled && unreadBase === null && msgsQ.data) setUnreadBase(msgsQ.data.read_seq);
  }, [settled, msgsQ.data, unreadBase]);

  const membersQ = useQuery({
    queryKey: ["conv-members", spaceSlug, conv.id],
    queryFn: () => api.listConversationMembers(spaceSlug, conv.id),
    enabled: conv.kind === "public" || conv.kind === "private",
  });
  const memberCount = membersQ.data?.members.length;
  const activeChannelTasks = useMemo(
    () => boot.tasks.filter((t) => t.channel_id === conv.id && t.status !== "done" && t.status !== "closed").length,
    [boot.tasks, conv.id],
  );
  const clearReply = useCallback(() => setReplyingTo(null), []);
  const quickEmoji = useQuickReactionEmoji();
  const memberByID = useMemo(() => new Map(boot.members.map((m) => [m.id, m])), [boot.members]);
  const streamEntries = useMemo(
    () => (messages ? buildStreamEntries(messages, boundary, boot.current_member.id) : []),
    [messages, boundary, boot.current_member.id],
  );

  const handleReplyToMessage = useCallback((messageID: string) => {
    setReplyingTo(messageID);
    composerFocusRef.current?.();
  }, []);
  const handleStartTask = useCallback((messageID: string) => {
    setNewTaskTitle("");
    setCreatingTaskOnMsg(messageID);
  }, []);
  const handleCancelTask = useCallback(() => setCreatingTaskOnMsg(null), []);
  const handleReactionPickerChange = useCallback((messageID: string, open: boolean) => {
    setReactionPickerMsgID(open ? messageID : null);
  }, []);
  const handleMoreMenuChange = useCallback((messageID: string, open: boolean) => {
    setMoreMenuMsgID(open ? messageID : null);
  }, []);
  const handleToggleReaction = useCallback(
    (messageID: string, emoji: ReactionEmoji, reacted: boolean) => {
      void reactions.toggle(messageID, emoji, reacted);
    },
    [reactions.toggle],
  );
  const handleCopyText = useCallback(
    (body: string) => {
      void navigator.clipboard?.writeText(body).then(() => toast("Copied message text", "success"));
    },
    [toast],
  );
  const handleCopyLink = useCallback(
    (messageID: string) => {
      const link = `${window.location.origin}${window.location.pathname}#${spaceSlug}/c/${conv.id}/m/${messageID}`;
      void navigator.clipboard?.writeText(link).then(() => toast("Copied message link", "success"));
    },
    [spaceSlug, conv.id, toast],
  );

  const handleCreateTask = useCallback(
    (e: FormEvent, sourceMsgID: string) => {
      e.preventDefault();
      if (!newTaskTitle.trim()) return;
      void taskAction.run(async () => {
        const res = await api.createTask(spaceSlug, { channel_id: conv.id, source_message_id: sourceMsgID, title: newTaskTitle.trim() });
        setNewTaskTitle("");
        setCreatingTaskOnMsg(null);
        onOpenDetail({ kind: "task", taskID: res.task.id });
        void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
        void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      });
    },
    [newTaskTitle, taskAction, spaceSlug, conv.id, onOpenDetail, qc],
  );

  return (
    <section className="flex flex-col h-full min-h-0 bg-panel">
      <header className="h-[52px] px-5 border-b border-line flex items-center justify-between gap-4 shrink-0 titlebar-drag">
        <div className="flex items-center gap-2.5 min-w-0">
          {conv.kind === "dm" ? (
            <MemberAvatar
              id={conv.peer_member_id ?? ""}
              name={title}
              kind={conv.peer_kind ?? "human"}
              size="sm"
              state={peer?.agent?.runtime_state}
            />
          ) : conv.kind === "private" ? (
            <Lock className="size-4 text-fg-3 shrink-0" />
          ) : (
            <Hash className="size-4 text-fg-3 shrink-0" />
          )}
          <h1 className="text-[15px] font-semibold text-fg shrink-0">{title}</h1>
          {isArchived && (
            <Badge tone="neutral">
              <Archive className="size-3" />
              Archived
            </Badge>
          )}
          {(conv.topic || (conv.kind === "dm" && peer?.agent?.role_text?.trim())) && (() => {
            const sub = conv.topic || peer!.agent!.role_text.replace(/\s+/g, " ").trim();
            return (
              <>
                <span className="w-px h-4 bg-line-strong shrink-0" />
                <span className="text-[13.5px] text-fg-3 truncate" title={sub}>{sub}</span>
              </>
            );
          })()}
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          {conv.kind !== "dm" && !isArchived && (
            <Menu
              trigger={(open, toggle) => {
                const cur = NOTIFY.find((n) => n.value === level);
                return (
                  <IconButton
                    label={`Notifications: ${cur?.label}`}
                    active={open}
                    aria-expanded={open}
                    onClick={toggle}
                    className={level === "mute" && !open ? "text-fg-4" : ""}
                  >
                    {cur?.icon}
                  </IconButton>
                );
              }}
            >
              {(close) =>
                NOTIFY.map((n) => (
                  <MenuItem
                    key={n.value}
                    active={n.value === level}
                    icon={n.icon}
                    hint={n.hint}
                    onSelect={() => {
                      close();
                      const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
                      if (prev) {
                        qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
                          ...prev,
                          conversations: prev.conversations.map((c) => (c.id === conv.id ? { ...c, notify_level: n.value } : c)),
                        });
                      }
                      void notify.run(async () => {
                        try {
                          await api.setNotifyLevel(spaceSlug, conv.id, n.value);
                        } catch (err) {
                          if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
                          throw err;
                        }
                        void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                      });
                    }}
                  >
                    {n.label}
                  </MenuItem>
                ))
              }
            </Menu>
          )}
          {isChannel && (
            <button
              onClick={() => onOpenDetail({ kind: "channel_tasks", convID: conv.id })}
              aria-label="Channel tasks"
              aria-pressed={activeDetail?.kind === "channel_tasks" && activeDetail.convID === conv.id}
              title="Channel tasks"
              className={`h-8 pl-2 pr-2.5 rounded-[4px] border flex items-center gap-1.5 text-[13px] transition-colors ${
                activeDetail?.kind === "channel_tasks" && activeDetail.convID === conv.id
                  ? "bg-subtle border-line-strong text-fg font-medium"
                  : "border-transparent text-fg-2 hover:bg-hover"
              }`}
            >
              <CheckCircle2 className="size-4 text-fg-3" />
              <span className="tabular min-w-[1ch] text-center">{activeChannelTasks}</span>
            </button>
          )}
          {isChannel && (
            <button
              onClick={() => onOpenDetail({ kind: "members", convID: conv.id })}
              aria-label="Members"
              aria-pressed={activeDetail?.kind === "members" && activeDetail.convID === conv.id}
              title="Members"
              className={`h-8 pl-2 pr-2.5 rounded-[4px] border flex items-center gap-1.5 text-[13px] transition-colors ${
                activeDetail?.kind === "members" && activeDetail.convID === conv.id
                  ? "bg-subtle border-line-strong text-fg font-medium"
                  : "border-transparent text-fg-2 hover:bg-hover"
              }`}
            >
              <Users className="size-4 text-fg-3" />
              <span className="tabular min-w-[1ch] text-center">{memberCount ?? "·"}</span>
            </button>
          )}
          {isChannel && !isArchived && (canGovern || (boot.channel_groups?.length ?? 0) > 0) && (
            <Menu
              width={236}
              trigger={(open, toggle) => (
                <IconButton label="Channel actions" active={open} onClick={toggle}>
                  <MoreHorizontal />
                </IconButton>
              )}
            >
              {(close) => {
                const groups = boot.channel_groups ?? [];
                const currentGroupID = groups.find((g) => g.conversation_ids.includes(conv.id))?.id ?? "";
                const moveGroup = (targetGroupID: string) => {
                  close();
                  const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
                  if (prev) {
                    qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
                      ...prev,
                      channel_groups: (prev.channel_groups ?? []).map((g) => {
                        const without = g.conversation_ids.filter((id) => id !== conv.id);
                        return g.id === targetGroupID ? { ...g, conversation_ids: [...without, conv.id] } : { ...g, conversation_ids: without };
                      }),
                    });
                  }
                  void notify.run(async () => {
                    try {
                      await api.setConversationGroup(spaceSlug, conv.id, targetGroupID);
                    } catch (err) {
                      if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
                      throw err;
                    }
                    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                  });
                };
                return (
                  <>
                    {groups.length > 0 && (
                      <div className="pb-1">
                        <div className="px-2.5 pt-1 pb-1 text-[11px] font-semibold uppercase tracking-[0.04em] text-fg-4">Move to group</div>
                        {groups.map((g) => (
                          <MenuItem key={g.id} active={currentGroupID === g.id} icon={<FolderInput />} onSelect={() => moveGroup(g.id)}>
                            {g.name}
                          </MenuItem>
                        ))}
                        {currentGroupID && (
                          <MenuItem icon={<X />} onSelect={() => moveGroup("")}>
                            Remove from group
                          </MenuItem>
                        )}
                        {canGovern && <div className="my-1 border-t border-line" />}
                      </div>
                    )}
                    {canGovern && (
                      <MenuItem
                        icon={<Archive className="text-danger" />}
                        hint="Make read-only and stop all notifications"
                        onSelect={() => {
                          close();
                          setConfirmArchive(true);
                        }}
                      >
                        Archive channel
                      </MenuItem>
                    )}
                  </>
                );
              }}
            </Menu>
          )}
          {peer?.kind === "agent" && (
            <IconButton
              label="Agent details"
              active={activeDetail?.kind === "agent" && activeDetail.memberID === peer.id}
              onClick={() => onOpenDetail({ kind: "agent", memberID: peer.id })}
            >
              <PanelRight />
            </IconButton>
          )}
        </div>
      </header>

      <div className="relative flex-1 min-h-0">
        <div ref={scrollRef} onScroll={onScroll} className="h-full overflow-y-auto scroll-thin">
          {msgsQ.isLoading ? (
            <MessageListSkeleton />
          ) : msgsQ.isError && !messages ? (
            <ErrorState
              title="Couldn't load messages"
              message={msgsQ.error instanceof Error ? msgsQ.error.message : "Check your connection and try again."}
              onRetry={() => void msgsQ.refetch()}
            />
          ) : !messages || messages.length === 0 ? (
            peer?.kind === "agent" ? (
              <EmptyState
                icon={<Sparkles />}
                title={`Say hi to ${title}`}
                body={`${title} replies in this DM. Describe what you need in one message — it reads the context it needs on its own.`}
              />
            ) : (
              <EmptyState
                icon={conv.kind === "dm" ? <MessageSquare /> : <Hash />}
                title={conv.kind === "dm" ? `Start a conversation with ${title}` : `Welcome to #${title}`}
                body={
                  conv.kind === "dm"
                    ? "Direct messages always notify the other person."
                    : `${memberCount !== undefined ? `${memberCount} members. ` : ""}@mention an agent to get it working, or turn any message into a Task.`
                }
              />
            )
          ) : (
            <div className="pb-4">
              <LoadEarlierButton hasMore={history.hasMore} loading={history.loading} onClick={() => void history.loadEarlier()} />
              {streamEntries.map((entry) => {
                if (entry.kind === "action_group") {
                  const first = entry.items[0];
                  const prev = messages[entry.firstIdx - 1];
                  const showDay = !prev || !sameDay(prev.created_at, first.created_at);
                  const showNew =
                    boundary > 0 &&
                    first.seq > boundary &&
                    first.author_member_id !== boot.current_member.id &&
                    (!prev || prev.seq <= boundary);
                  return (
                    <div key={entry.key}>
                      {showDay && <DayDivider iso={first.created_at} />}
                      {showNew && <UnreadDivider />}
                      <ActionMessageGroup items={entry.items} />
                    </div>
                  );
                }
                const m = entry.msg;
                const prev = messages[entry.idx - 1];
                const showDay = !prev || !sameDay(prev.created_at, m.created_at);
                const showNew = boundary > 0 && m.seq > boundary && m.author_member_id !== boot.current_member.id && (!prev || prev.seq <= boundary);
                const grouped = Boolean(
                  !showDay &&
                  !showNew &&
                  prev &&
                  prev.kind === "text" &&
                  m.kind === "text" &&
                  prev.author_member_id === m.author_member_id &&
                  new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() < GROUP_WINDOW_MS,
                );
                const creatingTask = creatingTaskOnMsg === m.id;
                return (
                  <MessageRow
                    key={m.id}
                    spaceSlug={spaceSlug}
                    convSlug={conv.slug ?? "dm"}
                    boot={boot}
                    m={m}
                    author={memberByID.get(m.author_member_id)}
                    grouped={grouped}
                    animateArrival={isNew(m.id)}
                    showDay={showDay}
                    showNew={showNew}
                    highlighted={replyingTo === m.id || highlightedMsgID === m.id}
                    isArchived={isArchived}
                    canGovern={canGovern}
                    creatingTask={creatingTask}
                    newTaskTitle={creatingTask ? newTaskTitle : ""}
                    taskBusy={creatingTask ? taskAction.busy : false}
                    reactionPickerOpen={reactionPickerMsgID === m.id}
                    moreMenuOpen={moreMenuMsgID === m.id}
                    quickEmoji={quickEmoji}
                    readAgents={agentReadMap.get(m.id)}
                    onOpenDetail={onOpenDetail}
                    onOpenDM={onOpenDM}
                    onReviewAgentProposal={onReviewAgentProposal}
                    onReply={handleReplyToMessage}
                    onStartTask={handleStartTask}
                    onChangeTaskTitle={setNewTaskTitle}
                    onCancelTask={handleCancelTask}
                    onSubmitTask={handleCreateTask}
                    onToggleReaction={handleToggleReaction}
                    onReactionPickerChange={handleReactionPickerChange}
                    onMoreMenuChange={handleMoreMenuChange}
                    onCopyText={handleCopyText}
                    onCopyLink={handleCopyLink}
                  />
                );
              })}
            </div>
          )}
        </div>
        <NewMessagesPill count={newCount} farFromEnd={farFromEnd} onClick={jumpToEnd} />
      </div>

      {isArchived ? (
        <div className="shrink-0 px-6 py-4 bg-bg/70 border-t border-line flex items-center justify-between gap-4">
          <div className="flex items-center gap-3 min-w-0">
            <span className="size-9 rounded-[6px] bg-subtle border border-line text-fg-3 flex items-center justify-center shrink-0">
              <Archive className="size-4" />
            </span>
            <div className="min-w-0">
              <div className="text-[14px] font-medium text-fg">This channel is archived</div>
              <div className="text-[12.5px] text-fg-3">No new messages, task updates, or notifications are allowed while archived.</div>
            </div>
          </div>
          {canGovern && (
            <Button
              variant="secondary"
              size="sm"
              icon={<ArchiveRestore />}
              loading={archiveAct.busy}
              onClick={() =>
                void archiveAct.run(async () => {
                  const res = await api.unarchiveChannel(spaceSlug, conv.id);
                  toast(`Unarchived #${conv.slug}`, "success");
                  await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                  await qc.invalidateQueries({ queryKey: ["archived-channels", spaceSlug] });
                  onUnarchived?.(res.conversation);
                })
              }
            >
              Unarchive
            </Button>
          )}
        </div>
      ) : (
      <div className="shrink-0 bg-panel">
        <AgentStatusLine spaceSlug={spaceSlug} boot={boot} conv={conv} onOpenDetail={onOpenDetail} onOpenConversation={onOpenConversation} />
        <MessageComposer
          spaceSlug={spaceSlug}
          boot={boot}
          conv={conv}
          title={title}
          replyingTo={replyingTo}
          replyTarget={replyTarget}
          onCancelReply={clearReply}
          onSent={jumpToEnd}
          focusRef={composerFocusRef}
        />
      </div>
      )}

      <Modal
        open={confirmArchive}
        onClose={() => setConfirmArchive(false)}
        label="Archive channel"
        title={`Archive #${conv.slug}?`}
        description="People and agents will no longer be able to post messages or receive notifications from this channel. You can unarchive it anytime in Settings."
        width={440}
      >
        <div className="px-6 pb-6 pt-4 flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => setConfirmArchive(false)}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="danger"
            icon={<Archive />}
            loading={archiveAct.busy}
            onClick={() =>
              void archiveAct.run(async () => {
                const res = await api.archiveChannel(spaceSlug, conv.id);
                setConfirmArchive(false);
                toast(`Archived #${conv.slug}`, "success");
                await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                await qc.invalidateQueries({ queryKey: ["archived-channels", spaceSlug] });
                onArchived?.(res.conversation);
              })
            }
          >
            Archive channel
          </Button>
        </div>
      </Modal>
    </section>
  );
}
