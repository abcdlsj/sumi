import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  ArrowUp,
  AtSign,
  Bell,
  BellOff,
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
  Sparkles,
  Users,
  X,
} from "lucide-react";
import { api, formatClock, formatDay, type BootstrapData, type Conversation, type Member, type Message, type NotifyLevel } from "../api";
import { memberDetailTarget, type DetailTarget } from "./DetailPane";
import { AgentFaceStack, AgentReadFaces, buildAgentReadMap, MemberHoverCard, WorkingAgentsPopover } from "./AgentVisibility";
import { agentFocus, agentStatusText, MemberAvatar, StatusDot } from "./Avatar";
import { AttachmentDraftRow, AttachmentPicker, MessageAttachments, useAttachmentDraft } from "./Attachments";
import { CollapsibleMessage } from "./CollapsibleMessage";
import { MentionPopup, useMentionMembers, useMentionPicker } from "./MentionPicker";
import { MessageBody } from "./MessageBody";
import { MessageReactions, QuickReactionButton, ReactionPickerButton, useReactionToggle } from "./Reactions";
import { ThreadReplySummary } from "./ThreadReplies";
import { LoadEarlierButton, mergeMessageIntoCache, NewMessagesPill, useOlderMessages, useStickToBottom } from "./scroll";
import { MessageListSkeleton } from "./Skeletons";
import { Badge, Button, EmptyState, ErrorState, IconButton, Menu, MenuItem, Modal, TaskStatusIcon, useAction, useToast } from "./ui";

const GROUP_WINDOW_MS = 5 * 60 * 1000;
const NOTIFY: { value: NotifyLevel; label: string; short: string; hint: string; icon: React.ReactNode }[] = [
  { value: "mentions", label: "Mentions only", short: "Mentions", hint: "Notify on @mentions and followed threads", icon: <AtSign /> },
  { value: "all", label: "All messages", short: "All", hint: "Every new message counts as unread", icon: <Bell /> },
  { value: "mute", label: "Muted", short: "Muted", hint: "Dim in sidebar; @mentions still reach you", icon: <BellOff /> },
];

function sameDay(a: string, b: string) {
  return new Date(a).toDateString() === new Date(b).toDateString();
}

/** Composer drafts survive conversation switches; the view is remounted per conversation. */
const drafts = new Map<string, { text: string; replyTo: string | null }>();

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

/** Honest runtime line above the composer: shows stacked working agents with a popover or DM peer state. */
function AgentStatusLine({
  boot,
  conv,
  onOpenDetail,
  onOpenConversation,
}: {
  boot: BootstrapData;
  conv: Conversation;
  onOpenDetail: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
}) {
  const [popOpen, setPopOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const agents = boot.members.filter((m) => m.kind === "agent");
  const peer = conv.kind === "dm" ? agents.find((a) => a.id === conv.peer_member_id) : undefined;
  const busyHere = agents.filter((a) => a.agent?.runtime_state === "working" && a.agent.focus_conversation_id === conv.id);

  if (busyHere.length > 0 && conv.kind !== "dm") {
    const names = busyHere.map((a) => a.display_name);
    const label =
      names.length === 1
        ? `${names[0]} is working on a reply…`
        : names.length === 2
          ? `${names[0]} and ${names[1]} are working here`
          : `${names[0]}, ${names[1]} and ${names.length - 2} ${names.length - 2 === 1 ? "other" : "others"} are working here`;
    return (
      <div className="h-7 px-5 pt-1 flex items-center gap-2 text-[12.5px] shrink-0" aria-live="polite">
        <button
          ref={triggerRef}
          type="button"
          onClick={() => (busyHere.length === 1 ? onOpenDetail({ kind: "agent", memberID: busyHere[0].id }) : setPopOpen((v) => !v))}
          className="min-w-0 inline-flex items-center gap-1.5 text-busy hover:underline underline-offset-2"
        >
          <AgentFaceStack agents={busyHere} ring="var(--panel)" />
          <span className="truncate font-medium">{label}</span>
        </button>
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
          />
        )}
      </div>
    );
  }

  const subject: Member | undefined = peer ?? busyHere[0];
  if (!subject?.agent) {
    return <div className="h-2 shrink-0" aria-live="polite" />;
  }
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

  return (
    <div className="h-7 px-5 pt-1 flex items-center gap-2 text-[12.5px] shrink-0" aria-live="polite">
      <StatusDot state={st} ring="var(--panel)" size={8} />
      <button onClick={() => onOpenDetail({ kind: "agent", memberID: subject.id })} className={`min-w-0 truncate hover:underline underline-offset-2 ${tone}`}>
        <span className="font-semibold">{subject.display_name}</span> {text}
      </button>
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
        className="flex items-center gap-2.5 text-[12.5px] text-fg-3 hover:text-fg transition-colors py-0.5"
      >
        <span className="w-8 flex justify-center shrink-0">
          <span className="size-5 rounded-full bg-subtle border border-line flex items-center justify-center">
            {open ? <ChevronDown className="size-3 text-fg-3" /> : <ChevronRight className="size-3 text-fg-3" />}
          </span>
        </span>
        <span className="font-medium text-fg-2">{items.length} updates</span>
        {summaryAuthors && <span className="text-fg-4 truncate">· {summaryAuthors}</span>}
        {lastAt && <span className="tabular text-[11.5px] text-fg-4 shrink-0">{formatClock(lastAt)}</span>}
      </button>
      {open && (
        <div className="mt-1 ml-11 pl-3 border-l border-line space-y-1 animate-fade-in">
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
    if (m.kind === "text") {
      entries.push({ kind: "message", msg: m, idx: i });
      i++;
      continue;
    }
    const run: Message[] = [m];
    let j = i + 1;
    while (j < messages.length) {
      const next = messages[j];
      if (next.kind === "text") break;
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
      <span className="flex-1 h-px bg-line/80" />
      <span className="h-6 px-2.5 rounded-[5px] bg-panel/95 backdrop-blur border border-line text-[11.5px] font-medium text-fg-3 inline-flex items-center shadow-sm">
        {formatDay(iso)}
      </span>
      <span className="flex-1 h-px bg-line/80" />
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
  const [composer, setComposer] = useState(() => drafts.get(conv.id)?.text ?? "");
  const draft = useAttachmentDraft(spaceSlug);
  const send = useAction();
  const textRef = useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    focusRef.current = () => textRef.current?.focus();
    textRef.current?.focus();
    return () => {
      focusRef.current = null;
    };
  }, [conv.id, focusRef]);

  useEffect(() => {
    drafts.set(conv.id, { text: composer, replyTo: replyingTo });
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
  const mentions = useMentionPicker({
    members: mentionMembers,
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

  return (
    <form onSubmit={(e) => void handleSend(e)} className="px-4 pb-4 pt-1">
      <div
        {...draft.dropProps}
        className={`relative bg-card border rounded-[8px] shadow-sm focus-within:border-accent focus-within:shadow-[var(--ring)] transition-[border-color,box-shadow,background-color] ${
          draft.dragging ? "border-accent bg-accent-soft/40" : "border-line-strong"
        }`}
      >
        <MentionPopup picker={mentions} />
        {replyTarget && (
          <div className="mx-2 mt-2 px-3 py-2 rounded-[6px] bg-subtle flex items-center gap-2 text-[13px] animate-fade-in">
            <CornerUpLeft className="size-3.5 text-fg-3 shrink-0" />
            <span className="text-fg-3 shrink-0">Replying in thread to</span>
            <span className="font-medium text-fg shrink-0">{replyTarget.author_display_name}</span>
            <span className="truncate text-fg-3">{replyTarget.body_markdown.slice(0, 140)}</span>
            <span className="flex-1" />
            <button type="button" onClick={onCancelReply} aria-label="Cancel thread reply" className="size-6 rounded-[5px] hover:bg-hover text-fg-3 flex items-center justify-center shrink-0">
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
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              void handleSend();
            }
            if (e.key === "Escape" && replyingTo) onCancelReply();
          }}
          placeholder={replyingTo ? "Reply in thread…" : `Message ${conv.kind === "dm" ? title : "#" + title}`}
          aria-label={replyingTo ? "Reply in thread" : `Message ${conv.kind === "dm" ? title : "#" + title}`}
          className="block w-full px-4 pt-3 pb-1 min-h-[48px] text-[14px] leading-relaxed bg-transparent outline-none resize-none placeholder:text-fg-4 scroll-thin"
        />
        <AttachmentDraftRow draft={draft} />
        <div className="px-2.5 pb-2.5 flex items-center justify-between gap-2">
          <AttachmentPicker draft={draft} />
          <div className="flex items-center gap-3">
            <span className="text-[12px] text-fg-4 hidden md:inline">
              <kbd className="font-sans">Enter</kbd> to send · <kbd className="font-sans">Shift + Enter</kbd> for a new line
            </span>
            <button
              type="submit"
              aria-label="Send"
              disabled={(!composer.trim() && draft.ids.length === 0) || draft.uploading || send.busy}
              className="size-8 rounded-[6px] bg-accent text-accent-fg hover:bg-accent-hover disabled:bg-subtle disabled:text-fg-4 flex items-center justify-center transition-colors active:scale-95"
            >
              <ArrowUp className="size-4" />
            </button>
          </div>
        </div>
      </div>
    </form>
  );
}

export function ConversationView({
  spaceSlug,
  boot,
  conv,
  onOpenDetail,
  onOpenConversation,
  onOpenDM,
  onArchived,
  onUnarchived,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  conv: Conversation;
  onOpenDetail: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
  onOpenDM?: (memberID: string) => void;
  onArchived?: (conv: Conversation) => void;
  onUnarchived?: (conv: Conversation) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [replyingTo, setReplyingTo] = useState<string | null>(() => drafts.get(conv.id)?.replyTo ?? null);
  const [creatingTaskOnMsg, setCreatingTaskOnMsg] = useState<string | null>(null);
  const [newTaskTitle, setNewTaskTitle] = useState("");
  const [reactionPickerMsgID, setReactionPickerMsgID] = useState<string | null>(null);
  const [moreMenuMsgID, setMoreMenuMsgID] = useState<string | null>(null);
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
  const clearReply = useCallback(() => setReplyingTo(null), []);

  async function handleCreateTask(e: FormEvent, sourceMsgID: string) {
    e.preventDefault();
    if (!newTaskTitle.trim()) return;
    await taskAction.run(async () => {
      const res = await api.createTask(spaceSlug, { channel_id: conv.id, source_message_id: sourceMsgID, title: newTaskTitle.trim() });
      setNewTaskTitle("");
      setCreatingTaskOnMsg(null);
      onOpenDetail({ kind: "task", taskID: res.task.id });
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

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
              trigger={(open, toggle) => (
                <button
                  onClick={toggle}
                  aria-expanded={open}
                  aria-label={`Notifications: ${NOTIFY.find((n) => n.value === level)?.label}`}
                  title={NOTIFY.find((n) => n.value === level)?.label}
                  className={`h-8 px-2 rounded-[6px] text-[13px] flex items-center gap-1.5 transition-colors [&>svg]:size-4 ${
                    open ? "bg-hover text-fg" : level === "mute" ? "text-fg-4 hover:bg-hover" : "text-fg-2 hover:bg-hover"
                  }`}
                >
                  {NOTIFY.find((n) => n.value === level)?.icon}
                  <span>{NOTIFY.find((n) => n.value === level)?.short}</span>
                  <ChevronDown className="size-3.5 text-fg-3" />
                </button>
              )}
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
              onClick={() => onOpenDetail({ kind: "members", convID: conv.id })}
              aria-label="Members"
              title="Members"
              className="h-8 pl-2 pr-2.5 rounded-[6px] flex items-center gap-1.5 text-[13px] text-fg-2 hover:bg-hover transition-colors"
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
            <IconButton label="Agent details" onClick={() => onOpenDetail({ kind: "agent", memberID: peer.id })}>
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
              {buildStreamEntries(messages, boundary, boot.current_member.id).map((entry) => {
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
                      {showNew && (
                        <div data-unread-anchor="new" className="px-6 py-2 flex items-center gap-3 animate-fade-in">
                          <div className="flex-1 h-px bg-warm/40" />
                          <span className="text-[12px] font-semibold text-warm">New messages</span>
                          <div className="flex-1 h-px bg-warm/40" />
                        </div>
                      )}
                      <ActionMessageGroup items={entry.items} />
                    </div>
                  );
                }
                const m = entry.msg;
                const prev = messages[entry.idx - 1];
                const showDay = !prev || !sameDay(prev.created_at, m.created_at);
                const showNew = boundary > 0 && m.seq > boundary && m.author_member_id !== boot.current_member.id && (!prev || prev.seq <= boundary);
                const grouped =
                  !showDay &&
                  !showNew &&
                  prev &&
                  prev.kind === "text" &&
                  m.kind === "text" &&
                  prev.author_member_id === m.author_member_id &&
                  new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() < GROUP_WINDOW_MS;
                const author = boot.members.find((x) => x.id === m.author_member_id);
                const toolbarPinned = reactionPickerMsgID === m.id || moreMenuMsgID === m.id;
                return (
                  <div key={m.id} data-message-id={m.id} className={isNew(m.id) ? "animate-rise-in" : ""}>
                    {showDay && <DayDivider iso={m.created_at} />}
                    {showNew && (
                      <div data-unread-anchor="new" className="px-6 py-2 flex items-center gap-3 animate-fade-in">
                        <div className="flex-1 h-px bg-warm/40" />
                        <span className="text-[12px] font-semibold text-warm">New messages</span>
                        <div className="flex-1 h-px bg-warm/40" />
                      </div>
                    )}
                    {m.kind !== "text" ? (
                      <div className="px-6 py-1.5 flex items-center gap-3 text-[13px] text-fg-3">
                        <span className="w-8 flex justify-center">
                          <span className="size-5 rounded-full bg-subtle border border-line flex items-center justify-center">
                            <Sparkles className="size-3 text-fg-3" />
                          </span>
                        </span>
                        <span className="min-w-0">
                          <span className="font-medium text-fg-2">{m.author_display_name}</span> {describeAction(m.action_kind, m.payload_json) ?? m.body_markdown}
                          <span className="ml-2 tabular text-[12px] text-fg-4">{formatClock(m.created_at)}</span>
                        </span>
                      </div>
                    ) : (
                      <article
                        className={`relative px-6 ${grouped ? "py-[3px]" : "pt-3.5 pb-1"} flex items-start gap-3.5 group transition-colors duration-100 ${
                          replyingTo === m.id ? "bg-accent-soft" : "hover:bg-subtle/60"
                        }`}
                      >
                        <div className="w-8 shrink-0">
                          {grouped ? (
                            <span className="block pt-[3px] text-right tabular text-[11px] leading-5 text-fg-4 opacity-0 group-hover:opacity-100 transition-opacity">
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
                        <div className="min-w-0 flex-1 max-w-[740px]">
                          {!grouped && (
                            <div className="flex items-center gap-2 h-5">
                              <MemberHoverCard
                                boot={boot}
                                member={author}
                                onInspect={(member) => onOpenDetail(memberDetailTarget(member))}
                                onMessage={onOpenDM}
                              >
                                <button
                                  type="button"
                                  onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
                                  className="text-[14px] font-semibold text-fg hover:underline underline-offset-2"
                                >
                                  {m.author_display_name}
                                </button>
                              </MemberHoverCard>
                              <span className="tabular text-[12px] text-fg-4">{formatClock(m.created_at)}</span>
                            </div>
                          )}
                          <div className={`${grouped ? "" : "mt-1"} text-[14.5px] leading-[1.65] text-fg md`}>
                            <CollapsibleMessage messageID={m.id}>
                              <MessageBody
                                body={m.body_markdown}
                                members={boot.members}
                                tasks={boot.tasks}
                                boot={boot}
                                selfID={boot.current_member.id}
                                onSelectMember={(member) => onOpenDetail(memberDetailTarget(member))}
                                onSelectTask={(tk) => onOpenDetail({ kind: "task", taskID: tk.id })}
                              />
                            </CollapsibleMessage>
                          </div>

                          <MessageAttachments spaceSlug={spaceSlug} attachments={m.attachments} />

                          <MessageReactions
                            reactions={m.reactions}
                            members={boot.members}
                            onToggle={(emoji, reacted) => void reactions.toggle(m.id, emoji, reacted)}
                          />

                          {m.attached_tasks && m.attached_tasks.length > 0 && (
                            <div className="mt-2.5 rounded-[7px] border border-line bg-card divide-y divide-line overflow-hidden max-w-[560px] shadow-sm">
                              {m.attached_tasks.map((tk) => (
                                <button
                                  key={tk.id}
                                  onClick={() => onOpenDetail({ kind: "task", taskID: tk.id })}
                                  className="w-full h-10 px-3 hover:bg-subtle flex items-center gap-2.5 text-left transition-colors"
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

                          {creatingTaskOnMsg === m.id && (
                            <form onSubmit={(e) => void handleCreateTask(e, m.id)} className="mt-2.5 flex items-center gap-2 max-w-[560px] animate-rise-in">
                              <input
                                autoFocus
                                value={newTaskTitle}
                                onChange={(e) => setNewTaskTitle(e.target.value)}
                                onKeyDown={(e) => e.key === "Escape" && setCreatingTaskOnMsg(null)}
                                placeholder="Task title"
                                className="flex-1 h-9 px-3 rounded-[6px] border border-accent bg-card text-[14px] outline-none shadow-[var(--ring)]"
                              />
                              <Button type="submit" size="sm" loading={taskAction.busy} disabled={!newTaskTitle.trim()}>
                                Create task
                              </Button>
                              <Button type="button" size="sm" variant="ghost" onClick={() => setCreatingTaskOnMsg(null)}>
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
                                onOpenDetail({ kind: "thread", threadConvID: m.thread_conversation_id!, rootMessageID: m.id, parentSlug: conv.slug ?? "dm" })
                              }
                            />
                          ) : null}

                          <AgentReadFaces
                            agents={agentReadMap.get(m.id)}
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
                            } transition-opacity duration-100 flex items-center gap-0.5 bg-card border border-line rounded-[6px] p-0.5 shadow-md z-20`}
                          >
                            <QuickReactionButton
                              reactions={m.reactions}
                              onToggle={(emoji, reacted) => void reactions.toggle(m.id, emoji, reacted)}
                            />
                            <ReactionPickerButton
                              reactions={m.reactions}
                              open={reactionPickerMsgID === m.id}
                              onOpenChange={(open) => setReactionPickerMsgID(open ? m.id : null)}
                              onToggle={(emoji, reacted) => void reactions.toggle(m.id, emoji, reacted)}
                            />
                            <button
                              onClick={() => {
                                setReplyingTo(m.id);
                                composerFocusRef.current?.();
                              }}
                              className="h-7 px-2 rounded-[5px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
                            >
                              <CornerUpLeft className="size-3.5" /> Reply
                            </button>
                            <button
                              onClick={() => {
                                setNewTaskTitle("");
                                setCreatingTaskOnMsg(m.id);
                              }}
                              className="h-7 px-2 rounded-[5px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
                            >
                              <ListPlus className="size-3.5" /> Task
                            </button>
                            <Menu
                              width={184}
                              onOpenChange={(open) => setMoreMenuMsgID(open ? m.id : null)}
                              trigger={(open, toggle) => (
                                <button
                                  type="button"
                                  aria-label="More message actions"
                                  aria-expanded={open}
                                  onClick={toggle}
                                  className={`size-7 rounded-[5px] text-fg-2 hover:bg-hover hover:text-fg flex items-center justify-center ${
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
                                      void navigator.clipboard?.writeText(m.body_markdown).then(() => toast("Copied message text", "success"));
                                    }}
                                  >
                                    Copy text
                                  </MenuItem>
                                  <MenuItem
                                    icon={<Link2 />}
                                    onSelect={() => {
                                      close();
                                      const link = `${window.location.origin}${window.location.pathname}#${spaceSlug}/c/${conv.id}/m/${m.id}`;
                                      void navigator.clipboard?.writeText(link).then(() => toast("Copied message link", "success"));
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
              })}
            </div>
          )}
        </div>
        <NewMessagesPill count={newCount} farFromEnd={farFromEnd} onClick={jumpToEnd} />
      </div>

      {isArchived ? (
        <div className="shrink-0 px-6 py-4 bg-bg/70 border-t border-line flex items-center justify-between gap-4">
          <div className="flex items-center gap-3 min-w-0">
            <span className="size-9 rounded-[7px] bg-subtle border border-line text-fg-3 flex items-center justify-center shrink-0">
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
      <div className="shrink-0 bg-bg/60 border-t border-line">
        <AgentStatusLine boot={boot} conv={conv} onOpenDetail={onOpenDetail} onOpenConversation={onOpenConversation} />
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
