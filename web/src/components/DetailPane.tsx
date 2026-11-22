import { useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ArrowUp, Bell, BellOff, Bookmark, CheckCircle2, ChevronDown, ChevronRight, ChevronUp, Copy, FolderOpen, Hash, Lock, MessageSquare, Monitor, MoreHorizontal, Pencil, Plus, RefreshCw, RotateCcw, Square, UserPlus, X } from "lucide-react";
import { saveAgentTemplate, templateFromAgent } from "../agentTemplates";
import { activityLabel, api, formatClock, formatDay, type AgentCreateProposal, type BootstrapData, type Member, type Message, type Permission, type Task, type TaskStatus } from "../api";
import { isComposerSendEvent, useSendKey, useTaskDetailsCollapsed } from "../preferences";
import { ActionCard, isActionCardMessage } from "./ActionCard";
import { AgentReadReceipts, buildAgentReadMap, ThreadOutlineRail } from "./AgentVisibility";
import { agentFocus, MemberAvatar, StatusDot, stateLabel } from "./Avatar";
import { AttachmentDraftRow, AttachmentPicker, MessageAttachments, useAttachmentDraft } from "./Attachments";
import { draftFromAgent, driverInput, DriverFields, validateDriver, type DriverDraft } from "./AgentForm";
import { AgentWorkspace } from "./AgentWorkspace";
import { describeAction } from "./ConversationView";
import { MentionPopup, useMentionMembers, useMentionPicker } from "./MentionPicker";
import { MessageBody } from "./MessageBody";
import { MessageReactions, ReactionPickerButton, useReactionToggle } from "./Reactions";
import { mergeMessageIntoCache, NewMessagesPill, useStickToBottom } from "./scroll";
import { DetailSkeleton, MessageListSkeleton } from "./Skeletons";
import { Badge, Button, EmptyState, ErrorState, IconButton, Menu, MenuItem, Segmented, Select, TaskStatusIcon, taskStatusLabel, useAction, useToast, type SelectOption } from "./ui";

export type DetailTarget =
  | { kind: "task"; taskID: string }
  | { kind: "thread"; threadConvID: string; rootMessageID: string; parentSlug: string }
  | { kind: "agent"; memberID: string; tab?: "overview" | "workspace"; filePath?: string; openNonce?: number }
  | { kind: "human"; memberID: string }
  | { kind: "members"; convID: string }
  | { kind: "channel_tasks"; convID: string };

export function memberDetailTarget(member: Pick<Member, "id" | "kind">): DetailTarget {
  return member.kind === "agent" ? { kind: "agent", memberID: member.id } : { kind: "human", memberID: member.id };
}

const PERMISSIONS: { code: Permission; label: string; hint: string }[] = [
  { code: "agent.create", label: "Create agents", hint: "Spin up new agents on its computer" },
  { code: "channel.create", label: "Create channels", hint: "Open new public or private channels" },
  { code: "channel.manage_members", label: "Manage channel members", hint: "Invite and remove members" },
];
const STATUSES: TaskStatus[] = ["todo", "in_progress", "in_review", "done", "closed"];

function PaneHeader({ children, onClose, actions }: { children: ReactNode; onClose: () => void; actions?: ReactNode }) {
  return (
    <header className="h-[52px] px-4 border-b border-line flex items-center justify-between gap-2 shrink-0 titlebar-drag">
      <div className="flex items-center gap-2 min-w-0 text-[14px]">{children}</div>
      <div className="flex items-center gap-1 shrink-0">
        {actions}
        <IconButton label="Close panel" onClick={onClose}>
          <X />
        </IconButton>
      </div>
    </header>
  );
}

function KV({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <span className="text-[13px] text-fg-3 h-8 flex items-center">{label}</span>
      <div className="min-w-0 min-h-8 flex items-center text-[14px] text-fg">{children}</div>
    </>
  );
}

function CompactMessage({
  m,
  spaceSlug,
  boot,
  onSelectTarget,
  onReviewAgentProposal,
  pickerOpen,
  onPickerOpenChange,
  onToggleReaction,
}: {
  m: Message;
  spaceSlug: string;
  boot: BootstrapData;
  onSelectTarget: (t: DetailTarget) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
  pickerOpen: boolean;
  onPickerOpenChange: (open: boolean) => void;
  onToggleReaction: (messageID: string, emoji: Parameters<typeof api.addReaction>[2], reacted: boolean) => void;
}) {
  const canGovern = boot.current_member.access_level !== "member";
  if (isActionCardMessage(m)) {
    const author = boot.members.find((x) => x.id === m.author_member_id);
    return (
      <article className="flex items-start gap-2.5">
        <button
          type="button"
          onClick={() => onSelectTarget(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
          aria-label={m.author_kind === "agent" ? `Inspect ${m.author_display_name}` : `View ${m.author_display_name}`}
          className="shrink-0"
        >
          <MemberAvatar
            id={m.author_member_id}
            name={m.author_display_name ?? "?"}
            kind={m.author_kind ?? "human"}
            size="sm"
            state={author?.agent?.runtime_state}
          />
        </button>
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 h-5">
            <button
              type="button"
              onClick={() => onSelectTarget(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
              className="text-[13.5px] font-semibold text-fg hover:underline underline-offset-2"
            >
              {m.author_display_name}
            </button>
            <span className="tabular text-[12px] text-fg-4">{formatClock(m.created_at)}</span>
          </div>
          <ActionCard
            spaceSlug={spaceSlug}
            message={m}
            canGovern={canGovern}
            onReviewAgentProposal={onReviewAgentProposal}
            onInspectAgent={(memberID) => onSelectTarget({ kind: "agent", memberID })}
          />
        </div>
      </article>
    );
  }
  if (m.kind !== "text") {
    return (
      <div className="flex items-center gap-2 text-[13px] text-fg-3 pl-1">
        <span className="size-1.5 rounded-full bg-fg-4 shrink-0" />
        <span className="min-w-0">
          <span className="font-medium text-fg-2">{m.author_display_name}</span> {describeAction(m.action_kind, m.payload_json) ?? m.body_markdown}
        </span>
        <span className="tabular text-[12px] text-fg-4 shrink-0">{formatClock(m.created_at)}</span>
      </div>
    );
  }
  const author = boot.members.find((x) => x.id === m.author_member_id);
  return (
    <article className="relative group flex items-start gap-2.5">
      <button
        type="button"
        onClick={() => onSelectTarget(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
        aria-label={m.author_kind === "agent" ? `Inspect ${m.author_display_name}` : `View ${m.author_display_name}`}
        className="shrink-0"
      >
        <MemberAvatar
          id={m.author_member_id}
          name={m.author_display_name ?? "?"}
          kind={m.author_kind ?? "human"}
          size="sm"
          state={author?.agent?.runtime_state}
        />
      </button>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 h-5">
          <button
            type="button"
            onClick={() => onSelectTarget(memberDetailTarget({ id: m.author_member_id, kind: m.author_kind ?? "human" }))}
            className="text-[13.5px] font-semibold text-fg hover:underline underline-offset-2"
          >
            {m.author_display_name}
          </button>
          <span className="tabular text-[12px] text-fg-4">{formatClock(m.created_at)}</span>
        </div>
        <div className="mt-0.5 text-[14px] leading-relaxed text-fg md">
          <MessageBody
            body={m.body_markdown}
            members={boot.members}
            tasks={boot.tasks}
            selfID={boot.current_member.id}
            onSelectMember={(member) => onSelectTarget(memberDetailTarget(member))}
            onSelectTask={(tk) => onSelectTarget({ kind: "task", taskID: tk.id })}
            onOpenWorkspaceFile={
              m.author_kind === "agent"
                ? (filePath) =>
                    onSelectTarget({
                      kind: "agent",
                      memberID: m.author_member_id,
                      tab: "workspace",
                      filePath,
                      openNonce: Date.now(),
                    })
                : undefined
            }
          />
        </div>
        <MessageAttachments spaceSlug={spaceSlug} attachments={m.attachments} />
        <MessageReactions
          reactions={m.reactions}
          members={boot.members}
          onToggle={(emoji, reacted) => onToggleReaction(m.id, emoji, reacted)}
        />
      </div>
      <div
        className={`absolute right-0 -top-2 ${
          pickerOpen
            ? "opacity-100 pointer-events-auto"
            : "opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus-within:opacity-100 focus-within:pointer-events-auto"
        } transition-opacity duration-100 bg-card border border-line rounded-[6px] p-0.5 shadow-sm z-20`}
      >
        <ReactionPickerButton
          reactions={m.reactions}
          open={pickerOpen}
          onOpenChange={onPickerOpenChange}
          onToggle={(emoji, reacted) => onToggleReaction(m.id, emoji, reacted)}
          compact
        />
      </div>
    </article>
  );
}

function ReplyBox({
  spaceSlug,
  placeholder,
  members,
  selfID,
  onSend,
}: {
  spaceSlug: string;
  placeholder: string;
  members: Member[];
  selfID: string;
  onSend: (body: string, attachmentIDs: string[]) => Promise<void>;
}) {
  const [reply, setReply] = useState("");
  const [sendKey] = useSendKey();
  const draft = useAttachmentDraft(spaceSlug);
  const { busy, run } = useAction();
  const textRef = useRef<HTMLTextAreaElement | null>(null);
  const mentions = useMentionPicker({ members, selfID, value: reply, onChange: setReply, inputRef: textRef });

  useLayoutEffect(() => {
    const el = textRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  }, [reply]);

  async function submit(e?: FormEvent) {
    e?.preventDefault();
    if ((!reply.trim() && draft.ids.length === 0) || draft.uploading || busy) return;
    const ok = await run(() => onSend(reply.trim() || `Attached ${draft.pending.map((a) => a.name).join(", ")}`, draft.ids));
    if (ok) {
      setReply("");
      draft.clear();
      mentions.close();
      textRef.current?.focus();
    }
  }

  return (
    <form onSubmit={submit} className="p-3 border-t border-line shrink-0">
      <div
        {...draft.dropProps}
        className={`relative rounded-[8px] border bg-card focus-within:border-accent focus-within:shadow-[var(--ring)] transition-[border-color,box-shadow,background-color] ${
          draft.dragging ? "border-accent bg-accent-soft/40" : "border-line-strong"
        }`}
      >
        <MentionPopup picker={mentions} />
        <AttachmentDraftRow draft={draft} />
        <div className="p-1.5 flex items-end gap-1">
          <AttachmentPicker draft={draft} />
          <textarea
            ref={textRef}
            rows={1}
            value={reply}
            onChange={(e) => {
              setReply(e.target.value);
              mentions.detect();
            }}
            onClick={mentions.detect}
            onPaste={draft.handlePaste}
            onBlur={mentions.close}
            onKeyDown={(e) => {
              if (mentions.handleKeyDown(e)) return;
              if (isComposerSendEvent(e, sendKey)) {
                e.preventDefault();
                void submit();
              }
            }}
            placeholder={placeholder}
            aria-label={placeholder}
            className="flex-1 min-h-8 px-1.5 py-1.5 text-[14px] leading-relaxed bg-transparent outline-none resize-none placeholder:text-fg-4 scroll-thin"
          />
          <button
            type="submit"
            aria-label="Send"
            disabled={busy || (!reply.trim() && draft.ids.length === 0) || draft.uploading}
            className="size-8 rounded-[6px] bg-accent text-accent-fg hover:bg-accent-hover disabled:bg-subtle disabled:text-fg-4 flex items-center justify-center transition-colors shrink-0"
          >
            <ArrowUp className="size-4" />
          </button>
        </div>
      </div>
    </form>
  );
}

export function DetailPane({
  spaceSlug,
  boot,
  target,
  onClose,
  onSelectTarget,
  onOpenConversation,
  onOpenDM,
  onCopyAgent,
  onReviewAgentProposal,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  target: DetailTarget;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
  onOpenDM?: (memberID: string) => void;
  onCopyAgent?: (agent: Member) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
}) {
  return (
    <aside className="h-full bg-panel border-l border-line flex flex-col min-h-0">
      {target.kind === "task" && (
        <TaskDetail
          spaceSlug={spaceSlug}
          boot={boot}
          taskID={target.taskID}
          onClose={onClose}
          onSelectTarget={onSelectTarget}
          onReviewAgentProposal={onReviewAgentProposal}
        />
      )}
      {target.kind === "thread" && (
        <ThreadDetail
          spaceSlug={spaceSlug}
          boot={boot}
          threadConvID={target.threadConvID}
          parentSlug={target.parentSlug}
          onClose={onClose}
          onSelectTarget={onSelectTarget}
          onReviewAgentProposal={onReviewAgentProposal}
        />
      )}
      {target.kind === "members" && <MembersDetail spaceSlug={spaceSlug} boot={boot} convID={target.convID} onClose={onClose} onSelectTarget={onSelectTarget} />}
      {target.kind === "channel_tasks" && <ChannelTasksDetail spaceSlug={spaceSlug} boot={boot} convID={target.convID} onClose={onClose} onSelectTarget={onSelectTarget} />}
      {target.kind === "agent" && (
        <AgentDetail
          key={target.memberID}
          spaceSlug={spaceSlug}
          boot={boot}
          memberID={target.memberID}
          initialTab={target.tab}
          initialFilePath={target.filePath}
          openNonce={target.openNonce}
          onClose={onClose}
          onSelectTarget={onSelectTarget}
          onOpenConversation={onOpenConversation}
          onOpenDM={onOpenDM}
          onCopyAgent={onCopyAgent}
        />
      )}
      {target.kind === "human" && (
        <HumanDetail
          boot={boot}
          memberID={target.memberID}
          onClose={onClose}
          onSelectTarget={onSelectTarget}
          onOpenDM={onOpenDM}
        />
      )}
    </aside>
  );
}

const CHANNEL_TASK_ORDER: TaskStatus[] = ["in_progress", "in_review", "todo", "done", "closed"];

function ChannelTasksDetail({
  spaceSlug,
  boot,
  convID,
  onClose,
  onSelectTarget,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  convID: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const [scope, setScope] = useState<"all" | "mine">("all");
  const [creating, setCreating] = useState(false);
  const [title, setTitle] = useState("");
  const [collapsed, setCollapsed] = useState<Record<TaskStatus, boolean>>({
    in_progress: false,
    in_review: false,
    todo: false,
    done: true,
    closed: true,
  });

  const conv = boot.conversations.find((c) => c.id === convID);
  const isArchived = !conv || Boolean(conv.archived_at);
  const tasksQ = useQuery({
    queryKey: ["channel-tasks", spaceSlug, convID],
    queryFn: () => api.listTasks(spaceSlug, "all").then((r) => r.tasks.filter((t) => t.channel_id === convID)),
  });
  const allChannelTasks = useMemo(
    () => tasksQ.data ?? boot.tasks.filter((t) => t.channel_id === convID),
    [tasksQ.data, boot.tasks, convID],
  );
  const myID = boot.current_member.id;
  const visible = useMemo(
    () => (scope === "mine" ? allChannelTasks.filter((t) => t.assignee_member_id === myID) : allChannelTasks),
    [allChannelTasks, scope, myID],
  );
  const activeCount = useMemo(
    () => allChannelTasks.filter((t) => t.status !== "done" && t.status !== "closed").length,
    [allChannelTasks],
  );
  const mineActiveCount = useMemo(
    () => allChannelTasks.filter((t) => t.assignee_member_id === myID && t.status !== "done" && t.status !== "closed").length,
    [allChannelTasks, myID],
  );
  const groups = useMemo(
    () =>
      CHANNEL_TASK_ORDER.map((status) => ({
        status,
        tasks: visible.filter((t) => t.status === status).sort((a, b) => b.number - a.number),
      })).filter((g) => g.tasks.length > 0),
    [visible],
  );

  async function submitNewTask(e: FormEvent) {
    e.preventDefault();
    if (!title.trim() || isArchived) return;
    await act.run(async () => {
      const res = await api.createTask(spaceSlug, { channel_id: convID, title: title.trim() });
      setTitle("");
      setCreating(false);
      void qc.invalidateQueries({ queryKey: ["channel-tasks", spaceSlug, convID] });
      void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      onSelectTarget({ kind: "task", taskID: res.task.id });
    });
  }

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          !isArchived ? (
            <Button size="sm" variant={creating ? "secondary" : "soft"} icon={<Plus />} onClick={() => setCreating((v) => !v)}>
              New
            </Button>
          ) : undefined
        }
      >
        <CheckCircle2 className="size-4 text-fg-3 shrink-0" />
        <span className="font-semibold text-fg truncate">#{conv?.slug}</span>
        <span className="text-fg-3">· Tasks</span>
      </PaneHeader>

      <div className="px-4 py-2.5 border-b border-line shrink-0 space-y-2.5 bg-panel">
        <Segmented
          full
          value={scope}
          onChange={setScope}
          options={[
            { value: "all", label: activeCount > 0 ? `All (${activeCount})` : "All" },
            { value: "mine", label: mineActiveCount > 0 ? `For me (${mineActiveCount})` : "For me" },
          ]}
        />
        {creating && (
          <form onSubmit={(e) => void submitNewTask(e)} className="flex items-center gap-1.5 animate-fade-in">
            <input
              autoFocus
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setCreating(false)}
              placeholder={`New task in #${conv?.slug ?? "channel"}…`}
              aria-label="New channel task title"
              className="flex-1 h-8 px-2.5 rounded-[6px] border border-accent bg-card text-[13.5px] outline-none shadow-[var(--ring)] placeholder:text-fg-4"
            />
            <Button type="submit" size="sm" loading={act.busy} disabled={!title.trim()}>
              Add
            </Button>
          </form>
        )}
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto scroll-thin p-3 space-y-3">
        {groups.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 />}
            title={scope === "mine" ? "Nothing assigned to you here" : `No tasks in #${conv?.slug ?? "channel"}`}
            body="Create a task here or hover any message in the channel to track it."
          />
        ) : (
          groups.map((g) => {
            const isClosed = collapsed[g.status];
            return (
              <div key={g.status}>
                <button
                  type="button"
                  aria-expanded={!isClosed}
                  onClick={() => setCollapsed((prev) => ({ ...prev, [g.status]: !prev[g.status] }))}
                  className="w-full h-7 px-1.5 rounded-[5px] hover:bg-subtle flex items-center gap-1.5 text-[12.5px] font-semibold text-fg-2 transition-colors"
                >
                  {isClosed ? <ChevronRight className="size-3.5 text-fg-4" /> : <ChevronDown className="size-3.5 text-fg-4" />}
                  <TaskStatusIcon status={g.status} size={13} />
                  <span>{taskStatusLabel(g.status)}</span>
                  <span className="tabular font-normal text-fg-4 ml-0.5">{g.tasks.length}</span>
                </button>
                {!isClosed && (
                  <div className="mt-1 rounded-[7px] border border-line bg-card divide-y divide-line overflow-hidden">
                    {g.tasks.map((t) => (
                      <button
                        key={t.id}
                        type="button"
                        onClick={() => onSelectTarget({ kind: "task", taskID: t.id })}
                        className="w-full px-3 py-2 hover:bg-subtle flex items-center gap-2 text-left transition-colors"
                      >
                        <span className="font-mono tabular text-[12px] text-fg-4 shrink-0">#{t.number}</span>
                        <span className="text-[13.5px] font-medium text-fg truncate flex-1">{t.title}</span>
                        {t.assignee_member_id && (
                          <MemberAvatar
                            id={t.assignee_member_id}
                            name={t.assignee_display_name ?? "?"}
                            kind={t.assignee_kind ?? "human"}
                            size="xs"
                          />
                        )}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            );
          })
        )}
      </div>
    </>
  );
}

function MembersDetail({
  spaceSlug,
  boot,
  convID,
  onClose,
  onSelectTarget,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  convID: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [q, setQ] = useState("");
  const conv = boot.conversations.find((c) => c.id === convID);
  const isArchived = !conv || Boolean(conv.archived_at);
  const membersQ = useQuery({
    queryKey: ["conv-members", spaceSlug, convID],
    queryFn: () => api.listConversationMembers(spaceSlug, convID),
  });
  const me = boot.current_member;
  const canManage = !isArchived && (me.access_level !== "member" || (me.permissions ?? []).includes("channel.manage_members"));
  const isPrivate = conv?.kind === "private";
  const members = membersQ.data?.members ?? [];
  const memberIDs = new Set(members.map((m) => m.id));
  const candidates = boot.members.filter((m) => !memberIDs.has(m.id) && m.display_name.toLowerCase().includes(q.trim().toLowerCase()));
  const byID = new Map(boot.members.map((m) => [m.id, m]));
  const refresh = () => {
    void qc.invalidateQueries({ queryKey: ["conv-members", spaceSlug, convID] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  };

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          canManage ? (
            <Button size="sm" variant={adding ? "secondary" : "soft"} icon={<UserPlus />} onClick={() => setAdding((v) => !v)}>
              Add
            </Button>
          ) : undefined
        }
      >
        {isPrivate ? <Lock className="size-4 text-fg-3" /> : <Hash className="size-4 text-fg-3" />}
        <span className="font-semibold text-fg truncate">{conv?.slug}</span>
        <span className="text-fg-3">· Members</span>
        {membersQ.data && <span className="font-mono text-[12px] text-fg-4">{members.length}</span>}
      </PaneHeader>
      {adding && (
        <div className="p-3 border-b border-line animate-fade-in">
          <input
            autoFocus
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Find people and agents"
            aria-label="Find members to add"
            className="w-full h-9 px-3 rounded-[6px] border border-line-strong bg-card text-[14px] outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
          />
          <div className="mt-2 max-h-[240px] overflow-y-auto scroll-thin space-y-0.5">
            {candidates.length === 0 ? (
              <p className="px-2 py-3 text-[13px] text-fg-3">{q ? "No one matches." : "Everyone in the space is already here."}</p>
            ) : (
              candidates.map((m) => (
                <button
                  key={m.id}
                  disabled={act.busy}
                  onClick={() =>
                    void act.run(async () => {
                      await api.inviteToChannel(spaceSlug, convID, m.id);
                      toast(`Added ${m.display_name} to #${conv?.slug}`, "success");
                      refresh();
                    })
                  }
                  className="w-full h-10 px-2 rounded-[6px] flex items-center gap-2.5 hover:bg-hover text-left disabled:opacity-60"
                >
                  <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" state={m.agent?.runtime_state} />
                  <span className="flex-1 truncate text-[14px] text-fg">{m.display_name}</span>
                  <span className="text-[12px] text-fg-4">{m.kind === "agent" ? "Agent" : m.access_level}</span>
                  <Plus className="size-4 text-fg-3" />
                </button>
              ))
            )}
          </div>
        </div>
      )}
      <div className="flex-1 min-h-0 overflow-y-auto scroll-thin p-3">
        {!isPrivate && (
          <p className="px-2 pb-3 text-[13px] text-fg-3 leading-snug">
            Public channel: people in the space can browse and join. Add agents explicitly to include them here.
          </p>
        )}
        {membersQ.isLoading ? (
          <div className="space-y-1">
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className="h-10 px-2 flex items-center gap-2.5">
                <span className="skeleton size-6 rounded-[5px]" />
                <span className="skeleton h-3.5 w-28" />
              </div>
            ))}
          </div>
        ) : membersQ.isError && !membersQ.data ? (
          <ErrorState
            compact
            title="Couldn't load members"
            message={membersQ.error instanceof Error ? membersQ.error.message : undefined}
            onRetry={() => void membersQ.refetch()}
          />
        ) : (
          <div className="space-y-0.5">
            {members.map((m) => {
              const full = byID.get(m.id) ?? m;
              const self = m.id === me.id;
              const canRemove = canManage || self;
              return (
                <div key={m.id} className="group h-10 px-2 rounded-[6px] flex items-center gap-2.5 hover:bg-subtle">
                  <button
                    className="flex items-center gap-2.5 min-w-0 flex-1 text-left"
                    onClick={() => onSelectTarget(memberDetailTarget(full))}
                  >
                    <MemberAvatar id={full.id} name={full.display_name} kind={full.kind} size="sm" state={full.agent?.runtime_state} />
                    <span className="truncate text-[14px] text-fg">{full.display_name}</span>
                    {self && <Badge>You</Badge>}
                  </button>
                  <div className="relative shrink-0 h-7 flex items-center justify-end min-w-[60px]">
                    <span className={`text-[12px] text-fg-4 ${canRemove ? "group-hover:opacity-0 group-focus-within:opacity-0 transition-opacity" : ""}`}>
                      {full.kind === "agent" ? "Agent" : full.access_level}
                    </span>
                    {canRemove && (
                      <button
                        onClick={() =>
                          void act.run(async () => {
                            await api.removeFromChannel(spaceSlug, convID, m.id);
                            toast(self ? `You left #${conv?.slug}` : `Removed ${full.display_name}`, "success");
                            refresh();
                            if (self && isPrivate) onClose();
                          })
                        }
                        className="absolute right-0 opacity-0 pointer-events-none group-hover:opacity-100 group-hover:pointer-events-auto focus-visible:opacity-100 focus-visible:pointer-events-auto h-7 px-2 rounded-[5px] text-[12.5px] text-fg-3 hover:text-danger hover:bg-danger-soft inline-flex items-center transition-opacity"
                      >
                        {self ? "Leave" : "Remove"}
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </>
  );
}

function TaskDetail({
  spaceSlug,
  boot,
  taskID,
  onClose,
  onSelectTarget,
  onReviewAgentProposal,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  taskID: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const [editing, setEditing] = useState(false);
  const [detailsCollapsed, setDetailsCollapsed] = useTaskDetailsCollapsed();
  const [editTitle, setEditTitle] = useState("");
  const [editDesc, setEditDesc] = useState("");
  const [pickerMsgID, setPickerMsgID] = useState<string | null>(null);
  const taskQ = useQuery({ queryKey: ["task", spaceSlug, taskID], queryFn: () => api.getTask(spaceSlug, taskID).then((r) => r.task) });
  const task = taskQ.data ?? boot.tasks.find((t) => t.id === taskID);
  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, task?.conversation_id],
    queryFn: () => api.listMessages(spaceSlug, task!.conversation_id!, true),
    enabled: Boolean(task?.conversation_id),
  });
  const reactions = useReactionToggle(spaceSlug, task?.conversation_id, undefined, boot.current_member.id);
  const taskMsgs = msgsQ.data?.messages;
  const lastTaskMsg = taskMsgs && taskMsgs.length > 0 ? taskMsgs[taskMsgs.length - 1] : undefined;
  const stream = useStickToBottom({
    streamKey: task?.conversation_id ?? taskID,
    count: taskMsgs?.length ?? 0,
    lastSeq: lastTaskMsg?.seq,
    lastId: lastTaskMsg?.id,
  });
  const members = useMentionMembers(spaceSlug, boot, task?.conversation_id ?? task?.channel_id);

  function refresh() {
    void qc.invalidateQueries({ queryKey: ["task", spaceSlug, taskID] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
    if (task?.conversation_id) void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, task.conversation_id] });
  }

  if (!task) {
    if (taskQ.isError) {
      return (
        <>
          <PaneHeader onClose={onClose}>
            <span className="font-semibold text-fg">Task</span>
          </PaneHeader>
          <ErrorState
            compact
            title="Couldn't load task"
            message={taskQ.error instanceof Error ? taskQ.error.message : undefined}
            onRetry={() => void taskQ.refetch()}
          />
        </>
      );
    }
    return <DetailSkeleton />;
  }
  const open = task.status !== "done" && task.status !== "closed";
  const meID = boot.current_member.id;
  const canClaim = open && (!task.assignee_member_id || (task.assignee_member_id === meID && task.status === "todo"));
  const canUnclaim = open && task.assignee_member_id === meID && task.status !== "todo";

  const assigneeMember = task.assignee_member_id ? boot.members.find((m) => m.id === task.assignee_member_id) : undefined;
  const assigneePool = members.slice();
  if (task.assignee_member_id && !assigneePool.some((m) => m.id === task.assignee_member_id)) {
    if (assigneeMember) assigneePool.push(assigneeMember);
  }
  const statusOptions: SelectOption<TaskStatus>[] = STATUSES.map((s) => ({
    value: s,
    label: taskStatusLabel(s),
    icon: (
      <span className="size-5 flex items-center justify-center">
        <TaskStatusIcon status={s} size={16} />
      </span>
    ),
  }));
  const assigneeOptions: SelectOption<string>[] = [
    { value: "", label: "Unassigned" },
    ...assigneePool.map((m) => ({
      value: m.id,
      label: m.display_name,
      description: m.kind === "agent" ? m.agent?.role_text || undefined : undefined,
      icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" state={m.agent?.runtime_state} />,
    })),
  ];

  function startEdit() {
    if (!task) return;
    setEditTitle(task.title);
    setEditDesc(task.description ?? "");
    setEditing(true);
  }

  async function saveEdit(e: FormEvent) {
    e.preventDefault();
    if (!task || !editTitle.trim()) return;
    const ok = await act.run(() =>
      api.amendTask(spaceSlug, task.id, {
        title: editTitle.trim(),
        description: editDesc,
        expected_revision: task.revision,
      }),
    );
    if (ok) {
      setEditing(false);
      refresh();
    }
  }

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          !editing ? (
            <Button size="sm" variant="secondary" icon={<Pencil />} onClick={startEdit}>
              Edit
            </Button>
          ) : undefined
        }
      >
        <TaskStatusIcon status={task.status} />
        <span className="tabular font-semibold text-fg">#{task.number}</span>
        <span className="text-fg-4">in</span>
        <span className="text-fg-2 truncate">{task.channel_slug ? `#${task.channel_slug}` : "Direct message"}</span>
      </PaneHeader>

      {detailsCollapsed && !editing ? (
        <section className="px-5 py-2.5 border-b border-line shrink-0">
          <button
            type="button"
            aria-expanded={false}
            aria-label="Expand task details"
            onClick={() => setDetailsCollapsed(false)}
            className="w-full text-left group flex items-start justify-between gap-2.5 rounded-[6px] -mx-1.5 px-1.5 py-1 hover:bg-subtle transition-colors"
          >
            <div className="min-w-0 flex-1">
              <div className="text-[14.5px] font-semibold text-fg truncate leading-snug">{task.title}</div>
              <div className="mt-1 flex items-center gap-2 text-[12.5px] text-fg-3 min-w-0 flex-wrap">
                <span className="inline-flex items-center gap-1.5 text-fg-2 font-medium shrink-0">
                  <TaskStatusIcon status={task.status} size={13} />
                  {taskStatusLabel(task.status)}
                </span>
                <span className="text-fg-4">·</span>
                {task.assignee_member_id ? (
                  <span className="inline-flex items-center gap-1.5 min-w-0">
                    <MemberAvatar
                      id={task.assignee_member_id}
                      name={task.assignee_display_name ?? "?"}
                      kind={task.assignee_kind ?? "human"}
                      size="xs"
                      state={assigneeMember?.agent?.runtime_state}
                    />
                    <span className="truncate text-fg-2">{task.assignee_display_name}</span>
                  </span>
                ) : (
                  <span className="text-fg-4">Unassigned</span>
                )}
                <span className="text-fg-4">·</span>
                <span className="tabular text-fg-4 shrink-0">rev {task.revision}</span>
              </div>
            </div>
            <span className="h-6 px-1.5 rounded-[5px] text-[12.5px] text-fg-3 group-hover:text-fg inline-flex items-center gap-1 shrink-0 mt-0.5">
              <ChevronDown className="size-3.5" />
              <span>Details</span>
            </span>
          </button>
        </section>
      ) : (
        <section className="px-5 py-3.5 border-b border-line space-y-2.5 shrink-0">
          {editing ? (
            <form onSubmit={(e) => void saveEdit(e)} className="space-y-2.5 animate-fade-in">
              <input
                autoFocus
                value={editTitle}
                onChange={(e) => setEditTitle(e.target.value)}
                aria-label="Task title"
                placeholder="Task title"
                className="w-full h-9 px-3 rounded-[6px] border border-line-strong bg-card text-[15px] font-semibold text-fg outline-none focus:border-accent focus:shadow-[var(--ring)]"
              />
              <textarea
                rows={3}
                value={editDesc}
                onChange={(e) => setEditDesc(e.target.value)}
                aria-label="Task description"
                placeholder="Add details…"
                className="w-full px-3 py-2 rounded-[6px] border border-line-strong bg-card text-[14px] text-fg leading-relaxed outline-none resize-none focus:border-accent focus:shadow-[var(--ring)]"
              />
              <div className="flex justify-end gap-2">
                <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>
                  Cancel
                </Button>
                <Button type="submit" size="sm" loading={act.busy} disabled={!editTitle.trim()}>
                  Save
                </Button>
              </div>
            </form>
          ) : (
            <>
              <div className="flex items-start justify-between gap-2">
                <h2 className="font-display text-[19px] font-semibold text-fg leading-snug min-w-0 flex-1">{task.title}</h2>
                <button
                  type="button"
                  aria-expanded={true}
                  aria-label="Collapse task details"
                  title="Collapse task details"
                  onClick={() => setDetailsCollapsed(true)}
                  className="h-7 px-2 -mr-1.5 rounded-[5px] text-[12.5px] text-fg-3 hover:text-fg hover:bg-hover inline-flex items-center gap-1 shrink-0 transition-colors"
                >
                  <ChevronUp className="size-3.5" />
                  <span>Hide</span>
                </button>
              </div>
              {task.description && (
                <p className="text-[14px] text-fg-2 leading-relaxed whitespace-pre-wrap max-h-[168px] overflow-y-auto scroll-thin pr-1">
                  {task.description}
                </p>
              )}
            </>
          )}
          <div className="grid grid-cols-[84px_1fr] gap-x-3 gap-y-2 pt-0.5">
            <KV label="Status">
              <Select
                size="sm"
                ariaLabel="Task status"
                value={task.status}
                options={statusOptions}
                onChange={(next) => {
                  const prevTask = qc.getQueryData<Task>(["task", spaceSlug, taskID]);
                  const prevBoot = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
                  if (prevTask) qc.setQueryData<Task>(["task", spaceSlug, taskID], { ...prevTask, status: next });
                  if (prevBoot) {
                    qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
                      ...prevBoot,
                      tasks: prevBoot.tasks.map((item) => (item.id === task.id ? { ...item, status: next } : item)),
                    });
                  }
                  void act.run(async () => {
                    try {
                      await api.updateTaskStatus(spaceSlug, task.id, next);
                    } catch (err) {
                      if (prevTask) qc.setQueryData(["task", spaceSlug, taskID], prevTask);
                      if (prevBoot) qc.setQueryData(["bootstrap", spaceSlug], prevBoot);
                      throw err;
                    }
                    refresh();
                  });
                }}
                className="w-[196px] max-w-full"
              />
            </KV>
            <KV label="Assignee">
              <div className="flex flex-wrap items-center gap-2 min-w-0 flex-1">
                <Select
                  size="sm"
                  searchable
                  searchPlaceholder="Search channel members…"
                  ariaLabel="Task assignee"
                  value={task.assignee_member_id ?? ""}
                  options={assigneeOptions}
                  disabled={!open}
                  onChange={(next) => {
                    const nextMember = assigneePool.find((m) => m.id === next);
                    const prevTask = qc.getQueryData<Task>(["task", spaceSlug, taskID]);
                    const prevBoot = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
                    const patch = {
                      assignee_member_id: next || undefined,
                      assignee_display_name: nextMember?.display_name,
                      assignee_kind: nextMember?.kind,
                    };
                    if (prevTask) qc.setQueryData<Task>(["task", spaceSlug, taskID], { ...prevTask, ...patch });
                    if (prevBoot) {
                      qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
                        ...prevBoot,
                        tasks: prevBoot.tasks.map((item) => (item.id === task.id ? { ...item, ...patch } : item)),
                      });
                    }
                    void act.run(async () => {
                      try {
                        await api.assignTask(spaceSlug, task.id, next);
                      } catch (err) {
                        if (prevTask) qc.setQueryData(["task", spaceSlug, taskID], prevTask);
                        if (prevBoot) qc.setQueryData(["bootstrap", spaceSlug], prevBoot);
                        throw err;
                      }
                      refresh();
                    });
                  }}
                  className="w-[196px] max-w-full"
                />
                {canClaim && (
                  <Button size="sm" variant="soft" loading={act.busy} onClick={() => void act.run(() => api.claimTask(spaceSlug, task.id)).then(refresh)}>
                    Claim
                  </Button>
                )}
                {canUnclaim && (
                  <Button size="sm" variant="ghost" loading={act.busy} onClick={() => void act.run(() => api.unclaimTask(spaceSlug, task.id)).then(refresh)}>
                    Unclaim
                  </Button>
                )}
              </div>
            </KV>
            <KV label="Revision">
              <span className="tabular text-[13px] text-fg-3">rev {task.revision}</span>
            </KV>
          </div>
        </section>
      )}

      <div className="h-9 px-5 flex items-center justify-between text-[13px] text-fg-3 border-b border-line shrink-0 bg-bg/50">
        <span className="font-semibold text-fg-2 flex items-center gap-2">
          <MessageSquare className="size-4" /> Discussion
        </span>
        <span className="tabular">{msgsQ.data?.messages.length ?? task.message_count} messages</span>
      </div>

      <div className="relative flex-1 min-h-0">
        <div ref={stream.ref} onScroll={stream.onScroll} className="h-full overflow-y-auto scroll-thin px-5 py-4 space-y-4">
          {task.conversation_id && msgsQ.isLoading ? (
            <MessageListSkeleton rows={3} compact />
          ) : task.conversation_id && msgsQ.isError && !msgsQ.data ? (
            <ErrorState
              compact
              title="Couldn't load discussion"
              message={msgsQ.error instanceof Error ? msgsQ.error.message : undefined}
              onRetry={() => void msgsQ.refetch()}
            />
          ) : !task.conversation_id || (msgsQ.data?.messages.length ?? 0) === 0 ? (
            <div className="py-12 text-center text-[14px] text-fg-3">No discussion yet. Replying starts the thread for #{task.number}.</div>
          ) : (
            msgsQ.data!.messages.map((m) => (
              <CompactMessage
                key={m.id}
                m={m}
                spaceSlug={spaceSlug}
                boot={boot}
                onSelectTarget={onSelectTarget}
                onReviewAgentProposal={onReviewAgentProposal}
                pickerOpen={pickerMsgID === m.id}
                onPickerOpenChange={(open) => setPickerMsgID(open ? m.id : null)}
                onToggleReaction={(id, emoji, reacted) => void reactions.toggle(id, emoji, reacted)}
              />
            ))
          )}
        </div>
        <NewMessagesPill count={stream.newCount} farFromEnd={stream.farFromEnd} onClick={stream.jumpToEnd} />
      </div>

      <ReplyBox
        spaceSlug={spaceSlug}
        placeholder={`Reply in #${task.number}…`}
        members={members}
        selfID={boot.current_member.id}
        onSend={async (body, ids) => {
          let convID = task.conversation_id;
          if (!convID) convID = (await api.ensureTaskConv(spaceSlug, task.id)).task.conversation_id;
          if (convID) {
            const res = await api.sendMessage(spaceSlug, convID, body, undefined, ids);
            if (res.message) mergeMessageIntoCache(qc, spaceSlug, convID, res.message);
          }
          stream.jumpToEnd();
          refresh();
        }}
      />
    </>
  );
}

function ThreadDetail({
  spaceSlug,
  boot,
  threadConvID,
  parentSlug,
  onClose,
  onSelectTarget,
  onReviewAgentProposal,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  threadConvID: string;
  parentSlug: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
  onReviewAgentProposal?: (proposal: AgentCreateProposal) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const [pickerMsgID, setPickerMsgID] = useState<string | null>(null);
  const msgsQ = useQuery({ queryKey: ["messages", spaceSlug, threadConvID], queryFn: () => api.listMessages(spaceSlug, threadConvID, true) });
  const reactions = useReactionToggle(spaceSlug, threadConvID, undefined, boot.current_member.id);
  const following = msgsQ.data?.following;
  const threadMsgs = msgsQ.data?.messages;
  const agentReadMap = useMemo(
    () => buildAgentReadMap(threadMsgs, msgsQ.data?.agent_reads, boot.members),
    [threadMsgs, msgsQ.data?.agent_reads, boot.members],
  );
  const [highlightedMsgID, setHighlightedMsgID] = useState<string | null>(null);
  const lastThreadMsg = threadMsgs && threadMsgs.length > 0 ? threadMsgs[threadMsgs.length - 1] : undefined;
  const stream = useStickToBottom({
    streamKey: threadConvID,
    count: threadMsgs?.length ?? 0,
    lastSeq: lastThreadMsg?.seq,
    lastId: lastThreadMsg?.id,
  });
  const members = useMentionMembers(spaceSlug, boot, threadConvID);

  function jumpToThreadMessage(messageID: string) {
    const el = stream.ref.current?.querySelector<HTMLElement>(`[data-thread-msg-id="${messageID}"]`);
    el?.scrollIntoView?.({ block: "center", behavior: "smooth" });
    setHighlightedMsgID(messageID);
    window.setTimeout(() => setHighlightedMsgID((cur) => (cur === messageID ? null : cur)), 1600);
  }

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          following === undefined ? null : (
            <Button
              size="sm"
              variant={following ? "secondary" : "soft"}
              icon={following ? <Bell /> : <BellOff />}
              loading={act.busy}
              onClick={() => {
                const nextFollowing = !following;
                const key = ["messages", spaceSlug, threadConvID];
                const prev = qc.getQueryData<{ messages: Message[]; read_seq: number; following: boolean; has_more?: boolean }>(key);
                if (prev) qc.setQueryData(key, { ...prev, following: nextFollowing });
                void act.run(async () => {
                  try {
                    await api.setFollowing(spaceSlug, threadConvID, nextFollowing);
                  } catch (err) {
                    if (prev) qc.setQueryData(key, prev);
                    throw err;
                  }
                  await qc.invalidateQueries({ queryKey: key });
                  void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
                });
              }}
            >
              {following ? "Following" : "Follow"}
            </Button>
          )
        }
      >
        <MessageSquare className="size-4 text-fg-3" />
        <span className="font-semibold text-fg">Thread</span>
        {parentSlug && (
          <span className="text-fg-3 truncate flex items-center gap-1">
            <Hash className="size-3.5" />
            {parentSlug}
          </span>
        )}
      </PaneHeader>
      <div className="relative flex-1 min-h-0">
        <div ref={stream.ref} onScroll={stream.onScroll} className="h-full overflow-y-auto scroll-thin pl-5 pr-7 py-4 space-y-4">
          {msgsQ.isLoading ? (
            <MessageListSkeleton rows={4} compact />
          ) : msgsQ.isError && !msgsQ.data ? (
            <ErrorState
              compact
              title="Couldn't load thread"
              message={msgsQ.error instanceof Error ? msgsQ.error.message : undefined}
              onRetry={() => void msgsQ.refetch()}
            />
          ) : (
            (msgsQ.data?.messages ?? []).map((m, i) => (
              <div
                key={m.id}
                data-thread-msg-id={m.id}
                className={`rounded-[6px] transition-colors duration-300 ${highlightedMsgID === m.id ? "bg-accent-soft/70 -mx-2 px-2 py-1" : ""}`}
              >
                <CompactMessage
                  m={m}
                  spaceSlug={spaceSlug}
                  boot={boot}
                  onSelectTarget={onSelectTarget}
                  onReviewAgentProposal={onReviewAgentProposal}
                  pickerOpen={pickerMsgID === m.id}
                  onPickerOpenChange={(open) => setPickerMsgID(open ? m.id : null)}
                  onToggleReaction={(id, emoji, reacted) => void reactions.toggle(id, emoji, reacted)}
                />
                <AgentReadReceipts
                  agents={agentReadMap.get(m.id)}
                  boot={boot}
                  onSelectAgent={(memberID) => onSelectTarget({ kind: "agent", memberID })}
                />
                {i === 0 && (msgsQ.data?.messages.length ?? 0) > 1 && (
                  <div className="mt-4 flex items-center gap-3 text-[12.5px] text-fg-3">
                    <span className="tabular">{(msgsQ.data?.messages.length ?? 1) - 1} replies</span>
                    <span className="flex-1 h-px bg-line" />
                  </div>
                )}
              </div>
            ))
          )}
        </div>
        <ThreadOutlineRail messages={threadMsgs ?? []} selfID={boot.current_member.id} onJump={jumpToThreadMessage} />
        <NewMessagesPill count={stream.newCount} farFromEnd={stream.farFromEnd} onClick={stream.jumpToEnd} />
      </div>
      <ReplyBox
        spaceSlug={spaceSlug}
        placeholder="Reply in thread…"
        members={members}
        selfID={boot.current_member.id}
        onSend={async (body, ids) => {
          const res = await api.sendMessage(spaceSlug, threadConvID, body, undefined, ids);
          if (res.message) mergeMessageIntoCache(qc, spaceSlug, threadConvID, res.message);
          stream.jumpToEnd();
          void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, threadConvID] });
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
        }}
      />
    </>
  );
}

function DriverEditor({ spaceSlug, member, boot, onDone }: { spaceSlug: string; member: Member; boot: BootstrapData; onDone: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [draft, setDraft] = useState<DriverDraft>(() => draftFromAgent(member));
  const [errors, setErrors] = useState<ReturnType<typeof validateDriver>>({});
  const { busy, run } = useAction();
  const computer = boot.computers.find((c) => c.id === member.agent?.computer_id);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const errs = validateDriver(draft);
        setErrors(errs);
        if (Object.keys(errs).length > 0) return;
        void run(async () => {
          await api.updateAgentDriver(spaceSlug, member.id, draft.kind, driverInput(draft));
          await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          toast(`${member.display_name} restarted with the new driver`, "success");
          onDone();
        });
      }}
      className="space-y-4 animate-fade-in"
    >
      <DriverFields
        draft={draft}
        onChange={setDraft}
        computer={computer}
        errors={errors}
        apiKeySet={member.agent?.driver_kind === "builtin" && member.agent.driver_config?.api_key_set}
      />
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" size="sm" loading={busy}>
          Save and restart
        </Button>
      </div>
    </form>
  );
}

function Section({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <section className="px-5 py-4">
      <div className="mb-2.5 flex items-center justify-between">
        <h3 className="text-[13px] font-semibold text-fg-2 uppercase tracking-[0.04em]">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  );
}

function AgentDetail({
  spaceSlug,
  boot,
  memberID,
  initialTab,
  initialFilePath,
  openNonce,
  onClose,
  onSelectTarget,
  onOpenConversation,
  onOpenDM,
  onCopyAgent,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  memberID: string;
  initialTab?: "overview" | "workspace";
  initialFilePath?: string;
  openNonce?: number;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
  onOpenConversation?: (convID: string) => void;
  onOpenDM?: (memberID: string) => void;
  onCopyAgent?: (agent: Member) => void;
}) {
  const qc = useQueryClient();
  const act = useAction();
  const toast = useToast();
  const [pending, setPending] = useState<"restart" | "reset_session" | "stop_turn" | null>(null);
  const [editing, setEditing] = useState(false);
  const [savingTemplate, setSavingTemplate] = useState(false);
  const [tplName, setTplName] = useState("");
  const [tab, setTab] = useState<"overview" | "workspace">(initialFilePath ? "workspace" : (initialTab ?? "overview"));

  useEffect(() => {
    if (initialFilePath) setTab("workspace");
    else if (initialTab) setTab(initialTab);
  }, [initialTab, initialFilePath, openNonce]);
  const member = boot.members.find((m) => m.id === memberID);
  if (!member?.agent) return <DetailSkeleton />;
  const ag = member.agent;
  const comp = boot.computers.find((c) => c.id === ag.computer_id);
  const canGovern = boot.current_member.access_level !== "member";
  const perms = new Set(member.permissions ?? []);
  const focus = agentFocus(boot, member);
  const st = ag.runtime_state;
  const cfg = ag.driver_config;

  async function control(action: "restart" | "reset_session" | "stop_turn") {
    setPending(action);
    await act.run(() => api.controlAgent(spaceSlug, member!.id, action));
    setPending(null);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  function submitTemplate(e: FormEvent) {
    e.preventDefault();
    const clean = tplName.trim();
    if (!clean) return;
    const saved = saveAgentTemplate(templateFromAgent(member!, clean));
    setSavingTemplate(false);
    setTplName("");
    toast(`Saved template “${saved.name}”`, "success");
  }

  const stateTone = st === "working" ? "busy" : st === "error" ? "danger" : "neutral";

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          <>
            {onOpenDM && (
              <Button size="sm" variant="secondary" icon={<MessageSquare />} onClick={() => onOpenDM(member.id)}>
                Message
              </Button>
            )}
            {canGovern && (
              <Menu
                width={224}
                trigger={(open, toggle) => (
                  <IconButton label="Agent actions" active={open} onClick={toggle}>
                    <MoreHorizontal />
                  </IconButton>
                )}
              >
                {(close) => (
                  <>
                    {onCopyAgent && (
                      <MenuItem
                        icon={<Copy />}
                        hint="Create a new agent from this config"
                        onSelect={() => {
                          close();
                          onCopyAgent(member);
                        }}
                      >
                        Copy to new agent
                      </MenuItem>
                    )}
                    <MenuItem
                      icon={<Bookmark />}
                      hint="Reuse this role and driver later"
                      onSelect={() => {
                        close();
                        setTplName(member.display_name);
                        setSavingTemplate(true);
                      }}
                    >
                      Save as template
                    </MenuItem>
                  </>
                )}
              </Menu>
            )}
          </>
        }
      >
        <span className="font-semibold text-fg">Agent</span>
      </PaneHeader>
      <div className="px-4 py-2 border-b border-line flex items-center gap-1 shrink-0 bg-panel">
        <button
          type="button"
          onClick={() => setTab("overview")}
          className={`h-7 px-3 rounded-[5px] text-[13px] font-medium transition-colors ${
            tab === "overview" ? "bg-subtle text-fg" : "text-fg-3 hover:text-fg hover:bg-hover"
          }`}
        >
          Overview
        </button>
        <button
          type="button"
          onClick={() => setTab("workspace")}
          className={`h-7 px-3 rounded-[5px] text-[13px] font-medium inline-flex items-center gap-1.5 transition-colors ${
            tab === "workspace" ? "bg-subtle text-fg" : "text-fg-3 hover:text-fg hover:bg-hover"
          }`}
        >
          <FolderOpen className="size-3.5" />
          Workspace
        </button>
      </div>
      {savingTemplate && (
        <form onSubmit={submitTemplate} className="px-4 py-2.5 border-b border-line bg-subtle/40 flex items-center gap-1.5 shrink-0 animate-fade-in">
          <input
            autoFocus
            value={tplName}
            onChange={(e) => setTplName(e.target.value)}
            onKeyDown={(e) => e.key === "Escape" && setSavingTemplate(false)}
            placeholder="Template name…"
            aria-label="Template name"
            maxLength={48}
            className="flex-1 h-8 px-2.5 rounded-[6px] border border-accent bg-card text-[13.5px] text-fg outline-none shadow-[var(--ring)] placeholder:text-fg-4"
          />
          <Button type="button" size="sm" variant="ghost" onClick={() => setSavingTemplate(false)}>
            Cancel
          </Button>
          <Button type="submit" size="sm" disabled={!tplName.trim()}>
            Save
          </Button>
        </form>
      )}
      {tab === "workspace" ? (
        <AgentWorkspace
          key={member.id}
          spaceSlug={spaceSlug}
          member={member}
          computer={comp}
          boot={boot}
          initialFilePath={initialFilePath}
          openNonce={openNonce}
        />
      ) : (
        <div className="flex-1 overflow-y-auto scroll-thin divide-y divide-line">
        <div className="px-5 py-5 flex items-center gap-4">
          <MemberAvatar id={member.id} name={member.display_name} kind="agent" size="xl" state={st} />
          <div className="min-w-0 flex-1">
            <div className="font-display text-[22px] font-semibold text-fg truncate">{member.display_name}</div>
            <div className="mt-1 flex items-center gap-1.5 flex-wrap">
              <Badge tone={stateTone}>
                <StatusDot state={st} size={7} ring="transparent" />
                {stateLabel(st)}
              </Badge>
              <Badge>{ag.driver_kind === "acp" ? `ACP · ${cfg?.provider ?? "?"}` : "Model API"}</Badge>
            </div>
          </div>
        </div>

        {st === "working" && (
          <div className="px-5 py-3.5 bg-busy-soft/60 flex items-center gap-3 animate-fade-in">
            <span className="size-8 rounded-[6px] bg-busy-soft text-busy flex items-center justify-center shrink-0">
              <RefreshCw className="size-4 animate-spin [animation-duration:2.4s]" />
            </span>
            <div className="min-w-0 flex-1">
              <div className="text-[12.5px] text-fg-3">Working in</div>
              {focus?.convID || focus?.taskID ? (
                <button
                  onClick={() => (focus.taskID ? onSelectTarget({ kind: "task", taskID: focus.taskID }) : onOpenConversation?.(focus.convID!))}
                  className="text-[14px] font-medium text-fg hover:underline underline-offset-2 truncate block max-w-full"
                >
                  {focus.label}
                </button>
              ) : (
                <div className="text-[14px] font-medium text-fg">{focus?.label ?? "Unknown conversation"}</div>
              )}
              {activityLabel(ag.activity) && (
                <div className={`mt-0.5 text-[12.5px] font-mono truncate ${ag.activity?.stalled_seconds ? "text-warm" : "text-fg-3"}`}>
                  {activityLabel(ag.activity)}
                </div>
              )}
            </div>
            {canGovern && (
              <Button
                variant="secondary"
                size="sm"
                icon={<Square className="size-3 fill-current" />}
                loading={pending === "stop_turn"}
                onClick={() => void control("stop_turn")}
              >
                Stop
              </Button>
            )}
          </div>
        )}

        {st === "error" && (
          <div className="px-5 py-3.5 bg-danger-soft flex items-start gap-3 animate-fade-in">
            <AlertTriangle className="size-4 text-danger shrink-0 mt-0.5" />
            <div className="min-w-0 text-[13.5px]">
              <div className="font-semibold text-danger font-mono">{ag.last_error_code || "error"}</div>
              {ag.last_error_detail && <div className="mt-1 text-fg-2 break-words leading-relaxed">{ag.last_error_detail}</div>}
              <div className="mt-1.5 text-fg-3">Fix the driver below or restart the agent.</div>
            </div>
          </div>
        )}

        <Section title="Runtime">
          <div className="grid grid-cols-[96px_1fr] gap-x-3">
            <KV label="Computer">
              {comp ? (
                <span className="flex items-center gap-2 min-w-0">
                  <Monitor className="size-4 text-fg-3 shrink-0" />
                  <span className="truncate">{comp.name}</span>
                  <span className={`size-1.5 rounded-full shrink-0 ${comp.connection_status === "online" ? "bg-online" : "bg-fg-4"}`} />
                </span>
              ) : (
                <span className="text-fg-3">Unassigned</span>
              )}
            </KV>
            <KV label="Session">1 active session</KV>
          </div>
          {canGovern && (
            <div className="mt-3 flex gap-2">
              <Button variant="secondary" size="sm" className="flex-1" icon={<RefreshCw />} loading={pending === "restart"} onClick={() => void control("restart")}>
                Restart
              </Button>
              <Button variant="secondary" size="sm" className="flex-1" icon={<RotateCcw />} loading={pending === "reset_session"} onClick={() => void control("reset_session")}>
                Reset session
              </Button>
            </div>
          )}
        </Section>

        <Section
          title="Driver"
          action={
            canGovern && !editing ? (
              <Button variant="ghost" size="sm" icon={<Pencil />} onClick={() => setEditing(true)}>
                Edit
              </Button>
            ) : null
          }
        >
          {editing ? (
            <DriverEditor spaceSlug={spaceSlug} member={member} boot={boot} onDone={() => setEditing(false)} />
          ) : (
            <div className="grid grid-cols-[96px_1fr] gap-x-3">
              <KV label="Type">{ag.driver_kind === "acp" ? "Coding agent (ACP)" : "Model API"}</KV>
              {ag.driver_kind === "acp" ? (
                <>
                  <KV label="Provider">
                    <span className="font-mono text-[13px]">{cfg?.provider || "—"}</span>
                  </KV>
                  {cfg?.command && cfg.command.length > 0 && (
                    <KV label="Command">
                      <span className="font-mono text-[13px] break-all">{cfg.command.join(" ")}</span>
                    </KV>
                  )}
                  {cfg?.pi_provider && (
                    <KV label="pi provider">
                      <span className="font-mono text-[13px]">{cfg.pi_provider}</span>
                    </KV>
                  )}
                  {cfg?.pi_model && (
                    <KV label="pi model">
                      <span className="font-mono text-[13px]">{cfg.pi_model}</span>
                    </KV>
                  )}
                  {cfg?.pi_packages && cfg.pi_packages.length > 0 && (
                    <KV label="pi packages">
                      <span className="font-mono text-[13px] break-all">{cfg.pi_packages.join(", ")}</span>
                    </KV>
                  )}
                </>
              ) : (
                <>
                  <KV label="Base URL">
                    <span className="font-mono text-[13px] truncate">{cfg?.base_url || "—"}</span>
                  </KV>
                  <KV label="Model">
                    <span className="font-mono text-[13px]">{cfg?.model || "—"}</span>
                  </KV>
                  <KV label="API key">{cfg?.api_key_set ? <Badge tone="work">Saved</Badge> : <span className="text-fg-3">Not set</span>}</KV>
                </>
              )}
            </div>
          )}
        </Section>

        <Section title="Role">
          <p className="text-[14px] text-fg-2 whitespace-pre-wrap leading-relaxed">{ag.role_text || "No role defined."}</p>
        </Section>

        <Section title="Permissions">
          <div className="rounded-[8px] border border-line divide-y divide-line overflow-hidden">
            {PERMISSIONS.map((p) => {
              const on = perms.has(p.code);
              return (
                <label key={p.code} className={`px-3.5 py-2.5 flex items-center gap-3 ${canGovern ? "cursor-pointer hover:bg-subtle" : ""}`}>
                  <span className="min-w-0 flex-1">
                    <span className="block text-[14px] text-fg">{p.label}</span>
                    <span className="block text-[12.5px] text-fg-3">{p.hint}</span>
                  </span>
                  <input
                    type="checkbox"
                    role="switch"
                    className="sr-only peer"
                    disabled={!canGovern}
                    checked={on}
                    onChange={() => {
                      const nextOn = !on;
                      const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
                      if (prev) {
                        qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
                          ...prev,
                          members: prev.members.map((item) => {
                            if (item.id !== member.id) return item;
                            const cur = new Set(item.permissions ?? []);
                            if (nextOn) cur.add(p.code);
                            else cur.delete(p.code);
                            return { ...item, permissions: Array.from(cur) };
                          }),
                        });
                      }
                      void act.run(async () => {
                        try {
                          await api.setPermission(spaceSlug, member.id, p.code, nextOn);
                        } catch (err) {
                          if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
                          throw err;
                        }
                        void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                      });
                    }}
                  />
                  <span
                    aria-hidden
                    className={`w-9 h-5 rounded-full p-0.5 transition-colors shrink-0 peer-focus-visible:shadow-[var(--ring)] ${on ? "bg-accent" : "bg-line-strong"} ${canGovern ? "" : "opacity-50"}`}
                  >
                    <span className={`block size-4 rounded-full bg-white shadow-sm transition-transform ${on ? "translate-x-4" : ""}`} />
                  </span>
                </label>
              );
            })}
          </div>
        </Section>
        </div>
      )}
    </>
  );
}

function HumanDetail({
  boot,
  memberID,
  onClose,
  onSelectTarget,
  onOpenDM,
}: {
  boot: BootstrapData;
  memberID: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
  onOpenDM?: (memberID: string) => void;
}) {
  const member = boot.members.find((m) => m.id === memberID);
  if (!member) return <DetailSkeleton />;
  const isSelf = member.id === boot.current_member.id;
  const computers = boot.computers.filter((c) => c.owner_member_id === member.id);
  const openTasks = boot.tasks.filter((t) => t.assignee_member_id === member.id && t.status !== "done" && t.status !== "closed");

  return (
    <>
      <PaneHeader
        onClose={onClose}
        actions={
          !isSelf && onOpenDM ? (
            <Button size="sm" variant="secondary" icon={<MessageSquare />} onClick={() => onOpenDM(member.id)}>
              Message
            </Button>
          ) : undefined
        }
      >
        <span className="font-semibold text-fg">Person</span>
      </PaneHeader>
      <div className="flex-1 overflow-y-auto scroll-thin divide-y divide-line">
        <div className="px-5 py-5 flex items-center gap-4">
          <MemberAvatar id={member.id} name={member.display_name} kind="human" size="xl" />
          <div className="min-w-0 flex-1">
            <div className="font-display text-[22px] font-semibold text-fg truncate">{member.display_name}</div>
            <div className="mt-1 flex items-center gap-1.5 flex-wrap">
              <Badge className="capitalize">{member.access_level}</Badge>
              {isSelf && <Badge tone="accent">You</Badge>}
            </div>
          </div>
        </div>

        <Section title="Profile">
          <div className="grid grid-cols-[96px_1fr] gap-x-3">
            <KV label="Handle">
              <span className="font-mono text-[13px]">@{member.display_name}</span>
            </KV>
            <KV label="Access">
              <span className="capitalize">{member.access_level}</span>
            </KV>
            {member.created_at && (
              <KV label="Joined">
                <span className="tabular text-fg-2">{formatDay(member.created_at)}</span>
              </KV>
            )}
          </div>
        </Section>

        {computers.length > 0 && (
          <Section title="Computers">
            <div className="space-y-1.5">
              {computers.map((c) => (
                <div key={c.id} className="h-9 px-2.5 rounded-[6px] bg-subtle/60 flex items-center gap-2 text-[13.5px]">
                  <Monitor className="size-4 text-fg-3 shrink-0" />
                  <span className="truncate flex-1 text-fg">{c.name}</span>
                  <span className={`size-1.5 rounded-full shrink-0 ${c.connection_status === "online" ? "bg-online" : "bg-fg-4"}`} />
                </div>
              ))}
            </div>
          </Section>
        )}

        {openTasks.length > 0 && (
          <Section title="Assigned tasks">
            <div className="rounded-[7px] border border-line bg-card divide-y divide-line overflow-hidden">
              {openTasks.map((tk) => (
                <button
                  key={tk.id}
                  onClick={() => onSelectTarget({ kind: "task", taskID: tk.id })}
                  className="w-full h-10 px-3 hover:bg-subtle flex items-center gap-2.5 text-left transition-colors"
                >
                  <TaskStatusIcon status={tk.status} />
                  <span className="font-mono text-[12.5px] text-fg-3 shrink-0">#{tk.number}</span>
                  <span className="text-[13.5px] font-medium text-fg truncate flex-1">{tk.title}</span>
                </button>
              ))}
            </div>
          </Section>
        )}
      </div>
    </>
  );
}
