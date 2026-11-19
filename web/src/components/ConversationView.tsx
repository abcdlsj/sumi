import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, BellOff, AtSign, CornerUpLeft, Hash, ListPlus, MessageSquare, X } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  api,
  formatClock,
  formatDay,
  type BootstrapData,
  type Conversation,
  type Member,
  type Message,
  type NotifyLevel,
} from "../api";
import type { DetailTarget } from "./DetailPane";
import { MemberAvatar, RuntimeDot, TaskStatusGlyph } from "./Identity";
import { AttachmentDraftRow, AttachmentPicker, MessageAttachments, useAttachmentDraft } from "./Attachments";
import { MessageListSkeleton } from "./Skeletons";
import { Button, EmptyState, Menu, MenuItem, useAction } from "./ui";

const GROUP_WINDOW_MS = 5 * 60 * 1000;
const NOTIFY: { value: NotifyLevel; label: string; hint: string }[] = [
  { value: "mentions", label: "Mentions only", hint: "Notify on @mentions and followed threads" },
  { value: "all", label: "All messages", hint: "Every new message counts as unread for you" },
  { value: "mute", label: "Muted", hint: "Dim in sidebar; @mentions still reach you" },
];

function sameDay(a: string, b: string) {
  return new Date(a).toDateString() === new Date(b).toDateString();
}

/** Keeps the stream pinned to the bottom unless the reader scrolled up. */
function useStickToBottom(dep: unknown, resetKey: string) {
  const ref = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);
  useLayoutEffect(() => {
    pinned.current = true;
  }, [resetKey]);
  useLayoutEffect(() => {
    const el = ref.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [dep, resetKey]);
  const onScroll = () => {
    const el = ref.current;
    if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  };
  return { ref, onScroll };
}

/** IDs present on first render are not animated; later arrivals rise in. */
function useArrivals(convID: string, messages: Message[] | undefined) {
  const seen = useRef<{ conv: string; ids: Set<string> | null }>({ conv: "", ids: null });
  if (seen.current.conv !== convID) seen.current = { conv: convID, ids: null };
  if (messages && seen.current.ids === null) seen.current.ids = new Set(messages.map((m) => m.id));
  const initial = seen.current.ids;
  return (id: string) => Boolean(initial && !initial.has(id));
}

function AgentStatusBar({
  boot,
  conv,
  onInspect,
  replyTarget,
  onCancelReply,
}: {
  boot: BootstrapData;
  conv: Conversation;
  onInspect: (memberID: string) => void;
  replyTarget?: Message;
  onCancelReply: () => void;
}) {
  if (replyTarget) {
    return (
      <div className="h-7 px-4 flex items-center justify-between gap-3 text-[12px] animate-fade-in">
        <span className="flex items-center gap-1.5 min-w-0 text-ink-2">
          <CornerUpLeft className="w-3.5 h-3.5 shrink-0" />
          <span className="shrink-0">Replying in thread to</span>
          <strong className="font-medium text-ink shrink-0">{replyTarget.author_display_name}</strong>
          <span className="truncate text-faint">{replyTarget.body_markdown.slice(0, 120)}</span>
        </span>
        <button onClick={onCancelReply} aria-label="Cancel thread reply" className="w-5 h-5 rounded hover:bg-hover text-mute flex items-center justify-center">
          <X className="w-3 h-3" />
        </button>
      </div>
    );
  }
  const agents = boot.members.filter((m) => m.kind === "agent");
  const peer = conv.kind === "dm" ? agents.find((a) => a.id === conv.peer_member_id) : undefined;
  const focusLabel = (a: Member) => {
    const t = boot.tasks.find((x) => x.conversation_id === a.agent?.focus_conversation_id);
    if (t) return ` on #${t.number}`;
    if (a.agent?.focus_conversation_id === conv.id) return " here";
    return "";
  };
  const subject = peer ?? agents.find((a) => a.agent?.runtime_state === "working") ?? agents.find((a) => a.agent?.runtime_state === "error");
  let text: React.ReactNode = <span className="text-faint">{agents.length === 0 ? "No agents in this space yet" : "All agents idle"}</span>;
  if (subject?.agent) {
    const st = subject.agent.runtime_state;
    const name = <strong className="font-medium text-ink">{subject.display_name}</strong>;
    text =
      st === "working" ? (
        <>
          {name} working{focusLabel(subject)}
          {peer && subject.agent.focus_conversation_id && subject.agent.focus_conversation_id !== conv.id && " · message queued"}
        </>
      ) : st === "error" ? (
        <span className="text-warm">
          {name} error · {subject.agent.last_error_code || "needs attention"}
        </span>
      ) : st === "offline" ? (
        <span className="text-faint">{name} offline · messages wait until its computer reconnects</span>
      ) : (
        <span className="text-faint">{name} idle</span>
      );
  }
  return (
    <div className="h-7 px-4 flex items-center text-[12px] text-ink-2">
      {subject ? (
        <button onClick={() => onInspect(subject.id)} className="inline-flex items-center gap-2 hover:text-ink min-w-0">
          <span className={subject.agent?.runtime_state === "working" ? "animate-breathe" : ""}>
            <RuntimeDot state={subject.agent?.runtime_state} />
          </span>
          <span className="truncate">{text}</span>
        </button>
      ) : (
        text
      )}
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
  const [composer, setComposer] = useState("");
  const [replyingTo, setReplyingTo] = useState<string | null>(null);
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
  const messages = msgsQ.data?.messages;
  const isNew = useArrivals(conv.id, messages);
  const { ref: scrollRef, onScroll } = useStickToBottom(messages?.length, conv.id);

  useEffect(() => {
    setReplyingTo(null);
    setCreatingTaskOnMsg(null);
    textRef.current?.focus();
  }, [conv.id]);

  // Auto-grow composer up to a fixed cap so the footer never jumps past it.
  useLayoutEffect(() => {
    const el = textRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 180)}px`;
  }, [composer]);

  const title = conv.kind === "dm" ? conv.peer_display_name ?? "Direct message" : conv.slug ?? "";
  const replyTarget = messages?.find((m) => m.id === replyingTo);

  async function handleSend(e?: FormEvent) {
    e?.preventDefault();
    const body = composer.trim();
    if ((!body && draft.ids.length === 0) || draft.uploading || send.busy) return;
    const ok = await send.run(() =>
      api.sendMessage(
        spaceSlug,
        conv.id,
        body || `Attached ${draft.pending.map((a) => a.name).join(", ")}`,
        replyingTo ?? undefined,
        draft.ids,
      ),
    );
    if (!ok) return;
    setComposer("");
    setReplyingTo(null);
    draft.clear();
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function handleCreateTask(e: FormEvent, sourceMsgID: string) {
    e.preventDefault();
    if (!newTaskTitle.trim()) return;
    await taskAction.run(async () => {
      const res = await api.createTask(spaceSlug, {
        channel_id: conv.id,
        source_message_id: sourceMsgID,
        title: newTaskTitle.trim(),
      });
      setNewTaskTitle("");
      setCreatingTaskOnMsg(null);
      onOpenDetail({ kind: "task", taskID: res.task.id });
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, conv.id] });
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

  const level = conv.notify_level ?? "mentions";
  const readSeq = msgsQ.data?.read_seq ?? 0;

  return (
    <section className="flex flex-col h-full min-h-0">
      <header className="h-11 px-4 border-b border-line flex items-center justify-between gap-4 shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          {conv.kind === "dm" ? (
            <MemberAvatar id={conv.peer_member_id ?? ""} name={title} kind={conv.peer_kind ?? "human"} size="sm" />
          ) : (
            <Hash className="w-3.5 h-3.5 text-mute shrink-0" />
          )}
          <h1 className="text-[14px] font-semibold text-ink shrink-0">{title}</h1>
          {conv.kind === "dm" && conv.peer_kind === "agent" && (
            <span className="px-1.5 rounded bg-paper text-[10.5px] text-mute border border-line">Agent</span>
          )}
          {conv.topic && (
            <>
              <span className="w-px h-3.5 bg-line-strong mx-1 shrink-0" />
              <span className="text-[12.5px] text-mute truncate">{conv.topic}</span>
            </>
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {conv.kind === "dm" && conv.peer_kind === "agent" && conv.peer_member_id && (
            <Button variant="ghost" size="xs" onClick={() => onOpenDetail({ kind: "agent", memberID: conv.peer_member_id! })}>
              Agent details
            </Button>
          )}
          {conv.kind !== "dm" && (
            <Menu
              trigger={(open, toggle) => (
                <button
                  onClick={toggle}
                  aria-expanded={open}
                  className={`h-7 px-2 rounded-[6px] border text-[12px] flex items-center gap-1.5 transition-colors ${
                    open ? "border-field bg-paper" : "border-line hover:bg-paper"
                  } ${level === "mute" ? "text-faint" : "text-ink-2"}`}
                >
                  {level === "mute" ? <BellOff className="w-3.5 h-3.5" /> : level === "all" ? <Bell className="w-3.5 h-3.5" /> : <AtSign className="w-3.5 h-3.5 text-warm" />}
                  <span>{NOTIFY.find((n) => n.value === level)?.label}</span>
                </button>
              )}
            >
              {(close) =>
                NOTIFY.map((n) => (
                  <MenuItem
                    key={n.value}
                    active={n.value === level}
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
        </div>
      </header>

      <div ref={scrollRef} onScroll={onScroll} className="flex-1 overflow-y-auto scroll-thin">
        {msgsQ.isLoading ? (
          <MessageListSkeleton />
        ) : !messages || messages.length === 0 ? (
          <EmptyState
            icon={conv.kind === "dm" ? <MessageSquare className="w-4.5 h-4.5" /> : <Hash className="w-4.5 h-4.5" />}
            title={conv.kind === "dm" ? `Start a conversation with ${title}` : `This is the start of #${title}`}
            body={
              conv.kind === "dm" && conv.peer_kind === "agent"
                ? "Direct messages always wake the agent. Describe what you need in one message."
                : "Post a message, then turn any message into a Task with + Task."
            }
          />
        ) : (
          <div className="py-3 animate-fade-in">
            {messages.map((m, idx) => {
              const prev = messages[idx - 1];
              const showDay = !prev || !sameDay(prev.created_at, m.created_at);
              const showNew =
                readSeq > 0 && m.seq > readSeq && m.author_member_id !== boot.current_member.id && (!prev || prev.seq <= readSeq);
              const grouped =
                !showDay &&
                !showNew &&
                prev &&
                prev.kind === "text" &&
                m.kind === "text" &&
                prev.author_member_id === m.author_member_id &&
                new Date(m.created_at).getTime() - new Date(prev.created_at).getTime() < GROUP_WINDOW_MS;
              return (
                <div key={m.id} className={isNew(m.id) ? "animate-rise-in" : ""}>
                  {showDay && (
                    <div className="px-5 pt-3 pb-1 flex items-center gap-3">
                      <div className="flex-1 h-px bg-line" />
                      <span className="text-[11px] font-medium text-faint">{formatDay(m.created_at)}</span>
                      <div className="flex-1 h-px bg-line" />
                    </div>
                  )}
                  {showNew && (
                    <div className="px-5 py-1.5 flex items-center gap-2.5 animate-fade-in">
                      <div className="flex-1 h-px bg-[#E3AE91]" />
                      <span className="text-[10.5px] font-semibold uppercase tracking-wider text-warm">New</span>
                      <div className="flex-1 h-px bg-[#E3AE91]" />
                    </div>
                  )}
                  {m.kind !== "text" ? (
                    <div className="px-5 py-1 flex items-center gap-3 text-[12px] text-mute">
                      <span className="w-7 flex justify-center">
                        <span className="w-1.5 h-1.5 rounded-full bg-work" />
                      </span>
                      <span>
                        <strong className="font-medium text-ink">{m.author_display_name}</strong>{" "}
                        {describeAction(m.action_kind, m.payload_json) ?? m.body_markdown}{" "}
                        <span className="font-mono-num text-[11px] text-faint">{formatClock(m.created_at)}</span>
                      </span>
                    </div>
                  ) : (
                    <article
                      className={`relative px-5 ${grouped ? "py-0.5" : "pt-2 pb-1"} flex items-start gap-3 group transition-colors duration-100 ${
                        replyingTo === m.id ? "bg-paper" : "hover:bg-paper-2"
                      }`}
                    >
                      <div className="w-7 shrink-0">
                        {grouped ? (
                          <span className="block pt-[3px] text-right font-mono-num text-[10px] text-faint opacity-0 group-hover:opacity-100 transition-opacity">
                            {formatClock(m.created_at)}
                          </span>
                        ) : (
                          <button
                            onClick={() => m.author_kind === "agent" && onOpenDetail({ kind: "agent", memberID: m.author_member_id })}
                            className={`mt-0.5 block ${m.author_kind === "agent" ? "" : "cursor-default"}`}
                            tabIndex={m.author_kind === "agent" ? 0 : -1}
                            aria-label={m.author_kind === "agent" ? `Inspect ${m.author_display_name}` : undefined}
                          >
                            <MemberAvatar id={m.author_member_id} name={m.author_display_name ?? "?"} kind={m.author_kind ?? "human"} />
                          </button>
                        )}
                      </div>
                      <div className="min-w-0 flex-1 max-w-[72ch]">
                        {!grouped && (
                          <div className="flex items-baseline gap-2">
                            <span className="text-[13.5px] font-semibold text-ink">{m.author_display_name}</span>
                            <span className="font-mono-num text-[11px] text-faint">{formatClock(m.created_at)}</span>
                          </div>
                        )}
                        <div className="mt-0.5 text-[13.5px] leading-[1.55] text-ink md break-words">
                          <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.body_markdown}</ReactMarkdown>
                        </div>

                        <MessageAttachments spaceSlug={spaceSlug} attachments={m.attachments} />

                        {m.attached_tasks && m.attached_tasks.length > 0 && (
                          <div className="mt-2 border border-line rounded-[6px] bg-paper-2 divide-y divide-line overflow-hidden max-w-[560px]">
                            {m.attached_tasks.map((tk) => (
                              <button
                                key={tk.id}
                                onClick={() => onOpenDetail({ kind: "task", taskID: tk.id })}
                                className="w-full h-8 px-2.5 hover:bg-[#F2F0EA] flex items-center justify-between gap-3 text-left transition-colors"
                              >
                                <span className="flex items-center gap-2 min-w-0">
                                  <TaskStatusGlyph status={tk.status} />
                                  <span className="font-mono-num text-[11.5px] text-mute w-7 shrink-0">#{tk.number}</span>
                                  <span className="text-[12.5px] font-medium text-ink truncate">{tk.title}</span>
                                </span>
                                <span className="flex items-center gap-3 shrink-0 text-[11.5px] text-mute">
                                  <span>{tk.assignee_display_name || "Unassigned"}</span>
                                  {tk.message_count > 0 && <span className="font-mono-num text-faint">{tk.message_count} msgs</span>}
                                </span>
                              </button>
                            ))}
                          </div>
                        )}

                        {creatingTaskOnMsg === m.id && (
                          <form onSubmit={(e) => void handleCreateTask(e, m.id)} className="mt-2 flex items-center gap-1.5 max-w-[560px] animate-rise-in">
                            <input
                              autoFocus
                              value={newTaskTitle}
                              onChange={(e) => setNewTaskTitle(e.target.value)}
                              onKeyDown={(e) => e.key === "Escape" && setCreatingTaskOnMsg(null)}
                              placeholder="Task title"
                              className="flex-1 h-7 px-2 rounded-[6px] border border-ink text-[12.5px] outline-none"
                            />
                            <Button type="submit" loading={taskAction.busy} disabled={!newTaskTitle.trim()}>
                              Create
                            </Button>
                            <Button type="button" variant="ghost" onClick={() => setCreatingTaskOnMsg(null)}>
                              Cancel
                            </Button>
                          </form>
                        )}

                        {m.reply_count && m.reply_count > 0 && m.thread_conversation_id ? (
                          <button
                            onClick={() =>
                              onOpenDetail({
                                kind: "thread",
                                threadConvID: m.thread_conversation_id!,
                                rootMessageID: m.id,
                                parentSlug: conv.slug ?? "dm",
                              })
                            }
                            className="mt-1 h-6 -ml-1.5 px-1.5 rounded-[5px] inline-flex items-center gap-2 text-[12px] text-ink-2 hover:bg-white hover:text-ink border border-transparent hover:border-line transition-colors"
                          >
                            <MessageSquare className="w-3 h-3 text-mute" />
                            <span className="font-medium">
                              {m.reply_count} {m.reply_count === 1 ? "reply" : "replies"}
                            </span>
                            <span className="font-mono-num text-[11px] text-faint">{formatClock(m.last_reply_at)}</span>
                          </button>
                        ) : null}
                      </div>

                      <div className="absolute right-4 -top-3 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity duration-100 flex items-center bg-white border border-line rounded-[6px] p-0.5">
                        <button
                          onClick={() => {
                            setReplyingTo(m.id);
                            textRef.current?.focus();
                          }}
                          title="Reply in thread"
                          className="h-6 px-1.5 rounded-[4px] text-[11.5px] text-ink-2 hover:bg-paper hover:text-ink flex items-center gap-1"
                        >
                          <CornerUpLeft className="w-3 h-3" /> Reply
                        </button>
                        <button
                          onClick={() => {
                            setNewTaskTitle("");
                            setCreatingTaskOnMsg(m.id);
                          }}
                          title="Create a Task from this message"
                          className="h-6 px-1.5 rounded-[4px] text-[11.5px] text-ink-2 hover:bg-paper hover:text-ink flex items-center gap-1"
                        >
                          <ListPlus className="w-3 h-3" /> Task
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

      <div className="border-t border-line bg-paper-2 shrink-0">
        <AgentStatusBar
          boot={boot}
          conv={conv}
          onInspect={(id) => onOpenDetail({ kind: "agent", memberID: id })}
          replyTarget={replyTarget}
          onCancelReply={() => setReplyingTo(null)}
        />
        <form onSubmit={(e) => void handleSend(e)} className="px-3 pb-3">
          <div className="bg-white border border-line-strong rounded-[8px] focus-within:border-ink transition-colors">
            <textarea
              ref={textRef}
              rows={1}
              value={composer}
              onChange={(e) => setComposer(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  void handleSend();
                }
                if (e.key === "Escape" && replyingTo) setReplyingTo(null);
              }}
              placeholder={replyingTo ? "Reply in thread…" : `Message ${conv.kind === "dm" ? title : "#" + title}`}
              className="block w-full px-3 pt-2.5 pb-1 min-h-[40px] text-[13.5px] bg-transparent outline-none resize-none placeholder:text-faint scroll-thin"
            />
            <AttachmentDraftRow draft={draft} />
            <div className="px-2 pb-2 flex items-center justify-between gap-2">
              <AttachmentPicker draft={draft} />
              <div className="flex items-center gap-2">
                <span className="font-mono-num text-[10.5px] text-faint hidden sm:inline">↵ send · ⇧↵ newline</span>
                <Button type="submit" size="xs" loading={send.busy} disabled={(!composer.trim() && draft.ids.length === 0) || draft.uploading}>
                  Send
                </Button>
              </div>
            </div>
          </div>
        </form>
      </div>
    </section>
  );
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
      if (p.op === "assigned") return `assigned #${p.task_number} to ${p.assignee_name ?? "nobody"}${p.reason ? `: ${p.reason}` : ""}`;
      if (p.op === "status_changed") return `moved #${p.task_number} ${p.from} → ${p.to}`;
      if (p.op === "amended") return `amended #${p.task_number} (rev ${p.revision})`;
    }
  } catch {
    // ignore malformed payload
  }
  return kind;
}
