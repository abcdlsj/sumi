import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowUp,
  AtSign,
  Bell,
  BellOff,
  ChevronDown,
  CornerUpLeft,
  Hash,
  ListPlus,
  Lock,
  MessageSquare,
  PanelRight,
  Sparkles,
  Users,
  X,
} from "lucide-react";
import { api, formatClock, formatDay, type BootstrapData, type Conversation, type Member, type Message, type NotifyLevel } from "../api";
import { memberDetailTarget, type DetailTarget } from "./DetailPane";
import { agentFocus, agentStatusText, MemberAvatar, StatusDot } from "./Avatar";
import { AttachmentDraftRow, AttachmentPicker, MessageAttachments, useAttachmentDraft } from "./Attachments";
import { MentionPopup, useMentionMembers, useMentionPicker } from "./MentionPicker";
import { MessageBody } from "./MessageBody";
import { LoadEarlierButton, NewMessagesPill, useOlderMessages, useStickToBottom } from "./scroll";
import { MessageListSkeleton } from "./Skeletons";
import { Button, EmptyState, IconButton, Menu, MenuItem, TaskStatusIcon, useAction } from "./ui";

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

/** Honest runtime line above the composer: who is working, where, or why not. */
function AgentStatusLine({
  boot,
  conv,
  onOpenDetail,
}: {
  boot: BootstrapData;
  conv: Conversation;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const agents = boot.members.filter((m) => m.kind === "agent");
  const peer = conv.kind === "dm" ? agents.find((a) => a.id === conv.peer_member_id) : undefined;
  const busyHere = agents.filter((a) => a.agent?.runtime_state === "working" && a.agent.focus_conversation_id === conv.id);
  const subject: Member | undefined = peer ?? busyHere[0];
  if (!subject?.agent) return null;
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
    <div className="px-5 pt-2.5 pb-1 flex items-center gap-2 text-[13px] animate-fade-in">
      <StatusDot state={st} ring="var(--bg)" size={8} />
      <button onClick={() => onOpenDetail({ kind: "agent", memberID: subject.id })} className={`min-w-0 truncate hover:underline underline-offset-2 ${tone}`}>
        <span className="font-semibold">{subject.display_name}</span> {text}
      </button>
      {busyHere.length > 1 && <span className="text-fg-3">+{busyHere.length - 1} more working</span>}
    </div>
  );
}

function DayDivider({ iso }: { iso: string }) {
  return (
    <div className="sticky top-0 z-10 py-2 flex justify-center pointer-events-none">
      <span className="h-6 px-3 rounded-full bg-panel/90 backdrop-blur border border-line text-[12px] font-medium text-fg-3 inline-flex items-center shadow-sm">
        {formatDay(iso)}
      </span>
    </div>
  );
}

export function ConversationView({
  spaceSlug,
  boot,
  conv,
  onOpenDetail,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  conv: Conversation;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const [composer, setComposer] = useState(() => drafts.get(conv.id)?.text ?? "");
  const [replyingTo, setReplyingTo] = useState<string | null>(() => drafts.get(conv.id)?.replyTo ?? null);
  const [creatingTaskOnMsg, setCreatingTaskOnMsg] = useState<string | null>(null);
  const [newTaskTitle, setNewTaskTitle] = useState("");
  const draft = useAttachmentDraft(spaceSlug);
  const send = useAction();
  const taskAction = useAction();
  const notify = useAction();
  const textRef = useRef<HTMLTextAreaElement | null>(null);

  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, conv.id],
    queryFn: () => api.listMessages(spaceSlug, conv.id, true),
  });
  const tailMessages = msgsQ.data?.messages;
  const isNew = useArrivals(conv.id, tailMessages);
  // Place the stream once the fresh page is here, not on a cached page that is about to change.
  const settled = msgsQ.isFetchedAfterMount || msgsQ.isError;
  const { ref: scrollRef, onScroll, newCount, jumpToEnd } = useStickToBottom({
    streamKey: conv.id,
    count: settled ? tailMessages?.length ?? 0 : 0,
    anchorId: "new",
  });
  const history = useOlderMessages({
    spaceSlug,
    convID: conv.id,
    latest: tailMessages,
    latestHasMore: msgsQ.data?.has_more,
    scrollRef,
  });
  const messages = history.messages;

  useEffect(() => {
    drafts.set(conv.id, { text: composer, replyTo: replyingTo });
  }, [conv.id, composer, replyingTo]);

  useEffect(() => {
    setCreatingTaskOnMsg(null);
    textRef.current?.focus();
  }, [conv.id]);

  useLayoutEffect(() => {
    const el = textRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 220)}px`;
  }, [composer]);

  const peer = conv.kind === "dm" ? boot.members.find((m) => m.id === conv.peer_member_id) : undefined;
  const title = conv.kind === "dm" ? conv.peer_display_name ?? "Direct message" : conv.slug ?? "";
  const replyTarget = messages?.find((m) => m.id === replyingTo);
  const level = conv.notify_level ?? "mentions";
  const readSeq = msgsQ.data?.read_seq ?? 0;
  // The unread boundary of this visit: later fetches advance the watermark, the divider stays put.
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
  const memberCount = membersQ.data?.members.length ?? boot.members.length;
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
    const ok = await send.run(() =>
      api.sendMessage(spaceSlug, conv.id, body || `Attached ${draft.pending.map((a) => a.name).join(", ")}`, replyingTo ?? undefined, draft.ids),
    );
    if (!ok) return;
    setComposer("");
    setReplyingTo(null);
    draft.clear();
    mentions.close();
    jumpToEnd();
    textRef.current?.focus();
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

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
      <header className="h-[52px] px-5 border-b border-line flex items-center justify-between gap-4 shrink-0">
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
          {conv.kind !== "dm" && (
            <Menu
              trigger={(open, toggle) => (
                <button
                  onClick={toggle}
                  aria-expanded={open}
                  aria-label={`Notifications: ${NOTIFY.find((n) => n.value === level)?.label}`}
                  title={NOTIFY.find((n) => n.value === level)?.label}
                  className={`h-8 px-2 rounded-[8px] text-[13px] flex items-center gap-1.5 transition-colors [&>svg]:size-4 ${
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
                      void notify.run(async () => {
                        await api.setNotifyLevel(spaceSlug, conv.id, n.value);
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
          {(conv.kind === "public" || conv.kind === "private") && (
            <button
              onClick={() => onOpenDetail({ kind: "members", convID: conv.id })}
              aria-label="Members"
              title="Members"
              className="h-8 pl-2 pr-2.5 rounded-[8px] flex items-center gap-1.5 text-[13px] text-fg-2 hover:bg-hover transition-colors"
            >
              <Users className="size-4 text-fg-3" />
              <span className="tabular">{memberCount}</span>
            </button>
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
                    : `${memberCount} members. @mention an agent to get it working, or turn any message into a Task.`
                }
              />
            )
          ) : (
            <div className="pb-4 animate-fade-in">
              <LoadEarlierButton hasMore={history.hasMore} loading={history.loading} onClick={() => void history.loadEarlier()} />
              {messages.map((m, idx) => {
                const prev = messages[idx - 1];
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
                return (
                  <div key={m.id} className={isNew(m.id) ? "animate-rise-in" : ""}>
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
                        className={`relative px-6 ${grouped ? "py-0.5" : "pt-3 pb-1"} flex items-start gap-3 group transition-colors duration-100 ${
                          replyingTo === m.id ? "bg-accent-soft" : "hover:bg-subtle/70"
                        }`}
                      >
                        <div className="w-8 shrink-0">
                          {grouped ? (
                            <span className="block pt-[3px] text-right tabular text-[11.5px] leading-5 text-fg-4 opacity-0 group-hover:opacity-100 transition-opacity">
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
                        <div className="min-w-0 flex-1 max-w-[760px]">
                          {!grouped && (
                            <div className="flex items-center gap-2 h-5">
                              <button
                                type="button"
                                onClick={() => onOpenDetail(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
                                className="text-[14px] font-semibold text-fg hover:underline underline-offset-2"
                              >
                                {m.author_display_name}
                              </button>
                              <span className="tabular text-[12px] text-fg-4">{formatClock(m.created_at)}</span>
                            </div>
                          )}
                          <div className={`${grouped ? "" : "mt-1"} text-[14px] leading-[1.6] text-fg md`}>
                            <MessageBody
                              body={m.body_markdown}
                              members={boot.members}
                              tasks={boot.tasks}
                              selfID={boot.current_member.id}
                              onSelectMember={(member) => onOpenDetail(memberDetailTarget(member))}
                              onSelectTask={(tk) => onOpenDetail({ kind: "task", taskID: tk.id })}
                            />
                          </div>

                          <MessageAttachments spaceSlug={spaceSlug} attachments={m.attachments} />

                          {m.attached_tasks && m.attached_tasks.length > 0 && (
                            <div className="mt-2.5 rounded-[10px] border border-line bg-card divide-y divide-line overflow-hidden max-w-[560px] shadow-sm">
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
                                className="flex-1 h-9 px-3 rounded-[9px] border border-accent bg-card text-[14px] outline-none shadow-[var(--ring)]"
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
                            <button
                              onClick={() =>
                                onOpenDetail({ kind: "thread", threadConvID: m.thread_conversation_id!, rootMessageID: m.id, parentSlug: conv.slug ?? "dm" })
                              }
                              className="mt-1.5 h-8 pl-2 pr-3 -ml-2 rounded-[8px] inline-flex items-center gap-2 text-[13px] text-accent-text hover:bg-accent-soft transition-colors"
                            >
                              <MessageSquare className="size-4" />
                              <span className="font-medium">
                                {m.reply_count} {m.reply_count === 1 ? "reply" : "replies"}
                              </span>
                              <span className="tabular text-[12px] text-fg-3">Last {formatClock(m.last_reply_at)}</span>
                            </button>
                          ) : null}
                        </div>

                        <div className="absolute right-5 -top-3.5 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity duration-100 flex items-center gap-0.5 bg-card border border-line rounded-[9px] p-0.5 shadow-md">
                          <button
                            onClick={() => {
                              setReplyingTo(m.id);
                              textRef.current?.focus();
                            }}
                            className="h-7 px-2 rounded-[7px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
                          >
                            <CornerUpLeft className="size-3.5" /> Reply
                          </button>
                          <button
                            onClick={() => {
                              setNewTaskTitle("");
                              setCreatingTaskOnMsg(m.id);
                            }}
                            className="h-7 px-2 rounded-[7px] text-[13px] text-fg-2 hover:bg-hover hover:text-fg flex items-center gap-1.5"
                          >
                            <ListPlus className="size-3.5" /> Task
                          </button>
                        </div>
                      </article>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>
        <NewMessagesPill count={newCount} onClick={jumpToEnd} />
      </div>

      <div className="shrink-0 bg-bg/60 border-t border-line">
        <AgentStatusLine boot={boot} conv={conv} onOpenDetail={onOpenDetail} />
        <form onSubmit={(e) => void handleSend(e)} className="px-4 pb-4 pt-2">
          <div
            {...draft.dropProps}
            className={`relative bg-card border rounded-[14px] shadow-sm focus-within:border-accent focus-within:shadow-[var(--ring)] transition-[border-color,box-shadow,background-color] ${
              draft.dragging ? "border-accent bg-accent-soft/40" : "border-line-strong"
            }`}
          >
            <MentionPopup picker={mentions} />
            {replyTarget && (
              <div className="mx-2 mt-2 px-3 py-2 rounded-[9px] bg-subtle flex items-center gap-2 text-[13px] animate-fade-in">
                <CornerUpLeft className="size-3.5 text-fg-3 shrink-0" />
                <span className="text-fg-3 shrink-0">Replying in thread to</span>
                <span className="font-medium text-fg shrink-0">{replyTarget.author_display_name}</span>
                <span className="truncate text-fg-3">{replyTarget.body_markdown.slice(0, 140)}</span>
                <span className="flex-1" />
                <button type="button" onClick={() => setReplyingTo(null)} aria-label="Cancel thread reply" className="size-6 rounded-[6px] hover:bg-hover text-fg-3 flex items-center justify-center shrink-0">
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
                if (e.key === "Escape" && replyingTo) setReplyingTo(null);
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
                  className="size-8 rounded-[9px] bg-accent text-accent-fg hover:bg-accent-hover disabled:bg-subtle disabled:text-fg-4 flex items-center justify-center transition-colors active:scale-95"
                >
                  <ArrowUp className="size-4" />
                </button>
              </div>
            </div>
          </div>
        </form>
      </div>
    </section>
  );
}
