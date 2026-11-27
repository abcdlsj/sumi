import { useRef, useState, type FormEvent, type ReactNode } from "react";
import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Archive,
  ArchiveRestore,
  AtSign,
  Bookmark,
  Bot,
  Check,
  CheckCheck,
  CheckCircle2,
  ChevronRight,
  CircleDot,
  Copy,
  Hash,
  Inbox,
  Layers,
  Lock,
  LogOut,
  MessageSquare,
  Monitor,
  Pencil,
  Plus,
  Search,
  SlidersHorizontal,
  Trash2,
  User,
  Users,
  X,
} from "lucide-react";
import { accentForeground, accentSwatch, SPACE_ACCENTS, type SpaceAccent } from "../accent";
import { templateDriverSummary, useAgentTemplates, type AgentTemplate } from "../agentTemplates";
import { activityLabel, api, formatClock, formatDay, formatShortTime, formatTimestamp, type BootstrapData, type Conversation, type InboxItem, type Member, type RuntimeState, type Task, type TaskStatus } from "../api";
import { isMacPlatform, useFontFamily, useFontSize, useLongMessagesPref, useSendKey, useSpellCheck, useSpellCheckAttr, type FontFamilyPref, type FontSizePref } from "../preferences";
import type { ThemePref } from "../theme";
import { AgentTemplateDialog } from "./AgentForm";
import type { DetailTarget } from "./DetailPane";
import { agentStatusText, MemberAvatar, StatusDot } from "./Avatar";
import { InboxSkeleton, RowListSkeleton } from "./Skeletons";
import { Badge, Button, EmptyState, ErrorState, Field, HeaderTabs, IconButton, Modal, Segmented, Select, TaskStatusIcon, taskStatusLabel, TextArea, useAction, useToast, type SelectOption } from "./ui";

export type InboxFilter = "needs_me" | "unread" | "all";
export type TaskFilter = "active" | "mine" | "done" | "closed";
export type AdminTab = "agents" | "computers" | "people" | "settings";

const REASON: Record<string, { label: string; icon: ReactNode }> = {
  dm: { label: "Direct message", icon: <MessageSquare /> },
  mention: { label: "Mentioned you", icon: <AtSign /> },
  task: { label: "Task", icon: <CircleDot /> },
  thread: { label: "Thread reply", icon: <MessageSquare /> },
  following: { label: "Following", icon: <MessageSquare /> },
  channel: { label: "Channel", icon: <Hash /> },
  channel_all: { label: "Channel", icon: <Hash /> },
};

export function ViewHeader({ title, icon, children, actions }: { title: string; icon?: ReactNode; children?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="h-[52px] px-6 border-b border-line flex items-center gap-4 shrink-0 bg-panel titlebar-drag">
      <h1 className="flex items-center gap-2 text-[15px] font-semibold text-fg [&>svg]:size-4 [&>svg]:text-fg-3">
        {icon}
        {title}
      </h1>
      {children}
      <span className="flex-1" />
      {actions}
    </header>
  );
}

export function InboxView({
  spaceSlug,
  filter,
  onFilter,
  onOpenConversation,
  onOpenDetail,
}: {
  spaceSlug: string;
  filter: InboxFilter;
  onFilter: (f: InboxFilter) => void;
  onOpenConversation: (convID: string) => void;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const { run } = useAction();
  const clearAllAct = useAction();
  const [leaving, setLeaving] = useState<Set<string>>(new Set());
  const inboxQ = useQuery({
    queryKey: ["inbox", spaceSlug, filter],
    queryFn: () => api.listInbox(spaceSlug, filter).then((r) => r.items),
    placeholderData: keepPreviousData,
  });

  function dismiss(it: InboxItem, fn: () => Promise<unknown>) {
    setLeaving((s) => new Set(s).add(it.conversation_id));
    void run(fn).then((ok) => {
      if (!ok)
        setLeaving((s) => {
          const n = new Set(s);
          n.delete(it.conversation_id);
          return n;
        });
      void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

  function open(it: InboxItem) {
    if (it.kind === "task" && it.task_id) onOpenDetail({ kind: "task", taskID: it.task_id });
    else if (it.kind === "thread") onOpenDetail({ kind: "thread", threadConvID: it.conversation_id, rootMessageID: "", parentSlug: it.parent_slug ?? "" });
    else onOpenConversation(it.conversation_id);
  }

  const items = inboxQ.data ?? [];

  async function markAllDone() {
    if (items.length === 0) return;
    const targets = items.map((it) => it.conversation_id);
    setLeaving((prev) => new Set([...prev, ...targets]));
    const ok = await clearAllAct.run(async () => {
      await Promise.all(targets.map((id) => api.markDone(spaceSlug, id)));
    });
    if (!ok) {
      setLeaving((prev) => {
        const next = new Set(prev);
        for (const id of targets) next.delete(id);
        return next;
      });
    } else {
      toast(`Marked ${targets.length} items as done`, "success");
    }
    void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  return (
    <section className="flex flex-col h-full min-h-0 bg-panel">
      <ViewHeader
        title="Inbox"
        icon={<Inbox />}
        actions={
          filter !== "all" && items.length > 1 ? (
            <Button
              size="sm"
              variant="secondary"
              icon={<CheckCheck />}
              loading={clearAllAct.busy}
              onClick={() => void markAllDone()}
            >
              Mark all done
            </Button>
          ) : undefined
        }
      >
        <span className="w-px h-4 bg-line shrink-0" />
        <HeaderTabs
          value={filter}
          onChange={onFilter}
          options={[
            { value: "needs_me", label: "Needs me" },
            { value: "unread", label: "Unread" },
            { value: "all", label: "All" },
          ]}
        />
      </ViewHeader>
      <div className="flex-1 overflow-y-auto scroll-thin">
        {inboxQ.isLoading ? (
          <InboxSkeleton />
        ) : inboxQ.isError && !inboxQ.data ? (
          <ErrorState
            title="Couldn't load inbox"
            message={inboxQ.error instanceof Error ? inboxQ.error.message : "Check your connection and try again."}
            onRetry={() => void inboxQ.refetch()}
          />
        ) : items.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 />}
            title="You're all caught up"
            body={filter === "needs_me" ? "DMs, @mentions, followed threads and Tasks assigned to you land here." : "Nothing unread right now."}
          />
        ) : (
          <div className={`divide-y divide-line border-b border-line transition-opacity duration-100 ${inboxQ.isPlaceholderData ? "opacity-65" : ""}`}>
            {items.map((it) => {
              const gone = leaving.has(it.conversation_id);
              const reason = REASON[it.reason] ?? { label: it.reason, icon: <Hash /> };
              return (
                <div
                  key={it.conversation_id}
                  className={`group px-6 py-3 flex items-center gap-3 hover:bg-subtle/60 transition-[opacity,transform,background-color] duration-150 ${
                    gone ? "opacity-0 translate-x-3 pointer-events-none" : ""
                  } ${it.needs_attention ? "border-l-[3px] border-l-accent pl-[21px]" : ""}`}
                >
                  <span
                    className={`size-8 rounded-[5px] flex items-center justify-center shrink-0 [&>svg]:size-4 ${
                      it.needs_attention ? "bg-warm-soft text-warm" : "bg-subtle text-fg-3"
                    }`}
                  >
                    {reason.icon}
                  </span>
                  <button className="min-w-0 flex-1 text-left" onClick={() => open(it)}>
                    <div className="flex items-center gap-2 text-[12.5px]">
                      <span className={`font-semibold ${it.needs_attention ? "text-warm" : "text-fg-2"}`}>{reason.label}</span>
                      <span className="text-fg-4">·</span>
                      <span className="text-fg-2 font-medium truncate">{it.title}</span>
                      {it.unread_count > 1 && <Badge>{it.unread_count} new</Badge>}
                    </div>
                    <p className="mt-0.5 text-[13.5px] text-fg truncate">
                      {it.latest_author_name && <span className="font-semibold">{it.latest_author_name}: </span>}
                      <span className="text-fg-2 font-body">{it.latest_preview}</span>
                    </p>
                  </button>
                  <span className="tabular text-[12px] text-fg-3 shrink-0 min-w-[48px] text-right" title={formatTimestamp(it.latest_at)}>
                    {formatShortTime(it.latest_at)}
                  </span>
                  <div className="flex items-center gap-1 shrink-0">
                    {(it.kind === "thread" || it.kind === "task") && it.following && (
                      <Button variant="ghost" size="sm" className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100" onClick={() => dismiss(it, () => api.setFollowing(spaceSlug, it.conversation_id, false))}>
                        Unfollow
                      </Button>
                    )}
                    {(it.kind === "public" || it.kind === "private") && it.notify_level !== "mute" && (
                      <Button variant="ghost" size="sm" className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100" onClick={() => dismiss(it, () => api.setNotifyLevel(spaceSlug, it.conversation_id, "mute"))}>
                        Mute
                      </Button>
                    )}
                    <Button variant="secondary" size="sm" icon={<CheckCircle2 />} onClick={() => dismiss(it, () => api.markDone(spaceSlug, it.conversation_id))}>
                      Done
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </section>
  );
}

const STATUS_ORDER: TaskStatus[] = ["in_progress", "in_review", "todo", "done", "closed"];

function TaskRow({ t, onOpen }: { t: Task; onOpen: () => void }) {
  return (
    <button onClick={onOpen} className="w-full min-h-11 px-6 py-2 hover:bg-subtle/60 flex items-center gap-3 text-left transition-colors">
      <TaskStatusIcon status={t.status} />
      <span className="font-mono tabular text-[12px] text-fg-3 w-10 shrink-0">#{t.number}</span>
      <span className="text-[13.5px] font-medium text-fg truncate flex-1 min-w-0">{t.title}</span>
      {t.message_count > 0 && (
        <span className="inline-flex items-center gap-1 text-[12px] text-fg-3 shrink-0">
          <MessageSquare className="size-3.5" />
          <span className="tabular">{t.message_count}</span>
        </span>
      )}
      <span className="w-28 text-[12.5px] text-fg-3 truncate shrink-0 text-right">{t.channel_slug ? `#${t.channel_slug}` : "DM"}</span>
      <span className="w-36 flex items-center gap-2 text-[13px] text-fg-2 shrink-0 min-w-0">
        {t.assignee_member_id ? (
          <>
            <MemberAvatar id={t.assignee_member_id} name={t.assignee_display_name ?? "?"} kind={t.assignee_kind ?? "human"} size="xs" />
            <span className="truncate">{t.assignee_display_name}</span>
          </>
        ) : (
          <span className="text-fg-4">Unassigned</span>
        )}
      </span>
    </button>
  );
}

function NewTaskModal({
  spaceSlug,
  boot,
  onClose,
  onCreated,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  onClose: () => void;
  onCreated: (task: Task) => void;
}) {
  const channels = boot.conversations.filter((c) => (c.kind === "public" || c.kind === "private") && !c.archived_at);
  const [channelID, setChannelID] = useState(channels[0]?.id ?? "");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [assigneeID, setAssigneeID] = useState("");
  const act = useAction();

  const membersQ = useQuery({
    queryKey: ["conv-members", spaceSlug, channelID],
    queryFn: () => api.listConversationMembers(spaceSlug, channelID).then((r) => r.members),
    enabled: Boolean(channelID),
  });
  const channelMembers = membersQ.data ?? boot.members.filter((m) => m.kind === "human");

  const channelOptions: SelectOption<string>[] = channels.map((c) => ({
    value: c.id,
    label: `#${c.slug ?? "channel"}`,
    description: c.topic || undefined,
    icon: (
      <span className="size-5 flex items-center justify-center text-fg-3">
        {c.kind === "private" ? <Lock className="size-3.5" /> : <Hash className="size-3.5" />}
      </span>
    ),
  }));

  const assigneeOptions: SelectOption<string>[] = [
    { value: "", label: "Unassigned" },
    ...channelMembers.map((m) => ({
      value: m.id,
      label: m.display_name,
      description: m.kind === "agent" ? m.agent?.role_text || undefined : undefined,
      icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" state={m.agent?.runtime_state} />,
    })),
  ];

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!channelID || !title.trim()) return;
    let created: Task | null = null;
    const ok = await act.run(async () => {
      const res = await api.createTask(spaceSlug, {
        channel_id: channelID,
        title: title.trim(),
        description: description.trim() || undefined,
        assignee_member_id: assigneeID || undefined,
      });
      created = res.task;
    });
    if (ok && created) onCreated(created);
  }

  return (
    <Modal open label="New task" title="New task" description="Track work inside a channel and optionally assign a member." onClose={onClose}>
      <form onSubmit={(e) => void submit(e)} className="px-6 pb-6 pt-4 space-y-4">
        <Field label="Title" autoFocus placeholder="e.g. Ship workspace file viewer" value={title} onChange={(e) => setTitle(e.target.value)} />
        <Select
          label="Channel"
          searchable
          searchPlaceholder="Filter channels…"
          value={channelID}
          options={channelOptions}
          onChange={(next) => {
            setChannelID(next);
            setAssigneeID("");
          }}
          placeholder="Select a channel"
        />
        <Select
          label="Assignee"
          optional
          searchable
          searchPlaceholder="Search channel members…"
          value={assigneeID}
          options={assigneeOptions}
          onChange={setAssigneeID}
          placeholder="Unassigned"
        />
        <TextArea label="Description" optional rows={3} placeholder="Context, acceptance criteria, or links…" value={description} onChange={(e) => setDescription(e.target.value)} />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={act.busy} disabled={!channelID || !title.trim()}>
            Create task
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function TasksView({
  spaceSlug,
  boot,
  filter,
  onFilter,
  onOpenDetail,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  filter: TaskFilter;
  onFilter: (f: TaskFilter) => void;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const spellCheck = useSpellCheckAttr();
  const [creating, setCreating] = useState(false);
  const [q, setQ] = useState("");
  const [channelFilter, setChannelFilter] = useState("");
  const statusQuery = filter === "done" ? "done" : filter === "closed" ? "closed" : "active";
  const tasksQ = useQuery({
    queryKey: ["tasks", spaceSlug, filter],
    queryFn: () => api.listTasks(spaceSlug, statusQuery, filter === "mine" ? "mine" : "").then((r) => r.tasks),
    placeholderData: keepPreviousData,
  });
  const allTasks = tasksQ.data ?? [];
  const query = q.trim().toLowerCase();
  const tasks = allTasks.filter((t) => {
    if (channelFilter && t.channel_id !== channelFilter) return false;
    if (!query) return true;
    return (
      t.title.toLowerCase().includes(query) ||
      `#${t.number}`.includes(query) ||
      (t.channel_slug ?? "").toLowerCase().includes(query) ||
      (t.assignee_display_name ?? "").toLowerCase().includes(query)
    );
  });
  const groups = STATUS_ORDER.map((s) => ({ status: s, tasks: tasks.filter((t) => t.status === s) })).filter((g) => g.tasks.length > 0);
  const activeChannels = boot.conversations.filter((c) => (c.kind === "public" || c.kind === "private") && !c.archived_at);
  const hasChannel = activeChannels.length > 0;
  const channelFilterOptions: SelectOption<string>[] = [
    { value: "", label: "All channels" },
    ...activeChannels.map((c) => ({
      value: c.id,
      label: `#${c.slug ?? "channel"}`,
      icon: c.kind === "private" ? <Lock className="size-3.5 text-fg-3" /> : <Hash className="size-3.5 text-fg-3" />,
    })),
  ];

  return (
    <section className="flex flex-col h-full min-h-0 bg-panel">
      <ViewHeader
        title="Tasks"
        icon={<CheckCircle2 />}
        actions={
          hasChannel ? (
            <Button size="sm" icon={<Plus />} onClick={() => setCreating(true)}>
              New task
            </Button>
          ) : undefined
        }
      >
        <span className="w-px h-4 bg-line shrink-0" />
        <HeaderTabs
          value={filter}
          onChange={onFilter}
          options={[
            { value: "active", label: "Active" },
            { value: "mine", label: "Assigned to me" },
            { value: "done", label: "Done" },
            { value: "closed", label: "Closed" },
          ]}
        />
      </ViewHeader>
      <div className="flex-1 overflow-y-auto scroll-thin">
        {allTasks.length > 0 && (
          <div className="px-6 py-2.5 border-b border-line bg-panel flex items-center gap-2.5 flex-wrap">
            <div className="relative flex-1 min-w-[200px] max-w-[320px]">
              <Search className="size-3.5 text-fg-4 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
              <input
                value={q}
                spellCheck={spellCheck}
                onChange={(e) => setQ(e.target.value)}
                placeholder="Filter by title, #number, or assignee…"
                aria-label="Filter tasks"
                className="w-full h-8 pl-8 pr-7 rounded-[5px] border border-line-strong bg-card text-[13px] outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
              />
              {q && (
                <button
                  type="button"
                  aria-label="Clear filter"
                  onClick={() => setQ("")}
                  className="absolute right-1.5 top-1/2 -translate-y-1/2 size-5 rounded-[4px] text-fg-4 hover:text-fg hover:bg-hover flex items-center justify-center"
                >
                  <X className="size-3.5" />
                </button>
              )}
            </div>
            {activeChannels.length > 1 && (
              <Select
                size="sm"
                ariaLabel="Filter by channel"
                value={channelFilter}
                options={channelFilterOptions}
                onChange={setChannelFilter}
                className="w-[180px]"
              />
            )}
          </div>
        )}
        {tasksQ.isLoading ? (
          <RowListSkeleton />
        ) : tasksQ.isError && !tasksQ.data ? (
          <ErrorState
            title="Couldn't load tasks"
            message={tasksQ.error instanceof Error ? tasksQ.error.message : "Check your connection and try again."}
            onRetry={() => void tasksQ.refetch()}
          />
        ) : groups.length === 0 ? (
          allTasks.length > 0 ? (
            <div className="px-6 py-14 text-center">
              <p className="text-[14px] text-fg-2 font-medium">No tasks match the current filter</p>
              <div className="mt-3">
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => {
                    setQ("");
                    setChannelFilter("");
                  }}
                >
                  Reset filters
                </Button>
              </div>
            </div>
          ) : (
            <EmptyState
              icon={<CheckCircle2 />}
              title={
                filter === "done"
                  ? "No finished tasks yet"
                  : filter === "closed"
                    ? "No closed tasks"
                    : filter === "mine"
                      ? "Nothing assigned to you"
                      : "No active tasks"
              }
              body="Create a task directly here, or hover any message in a channel to turn it into tracked work."
              action={
                hasChannel ? (
                  <Button size="sm" icon={<Plus />} onClick={() => setCreating(true)}>
                    New task
                  </Button>
                ) : undefined
              }
            />
          )
        ) : (
          <div>
            <div className="h-8 px-6 bg-subtle/60 border-b border-line flex items-center gap-3 text-[11px] font-semibold uppercase tracking-[0.05em] text-fg-3 select-none">
              <span className="size-4 shrink-0" />
              <span className="w-10 shrink-0">ID</span>
              <span className="flex-1 min-w-0">Task</span>
              <span className="w-28 shrink-0 text-right">Channel</span>
              <span className="w-36 shrink-0">Assignee</span>
            </div>
            {groups.map((g) => (
              <div key={g.status}>
                <div className="h-8 px-6 bg-subtle/85 border-b border-line flex items-center gap-2 text-[12px] font-semibold text-fg-2">
                  <TaskStatusIcon status={g.status} size={14} />
                  <span>{taskStatusLabel(g.status)}</span>
                  <span className="ml-auto font-mono font-normal text-[11.5px] text-fg-4 tabular">{g.tasks.length}</span>
                </div>
                <div className="divide-y divide-line border-b border-line">
                  {g.tasks.map((t) => (
                    <TaskRow key={t.id} t={t} onOpen={() => onOpenDetail({ kind: "task", taskID: t.id })} />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
      {creating && (
        <NewTaskModal
          spaceSlug={spaceSlug}
          boot={boot}
          onClose={() => setCreating(false)}
          onCreated={(created) => {
            setCreating(false);
            void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
            void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
            onOpenDetail({ kind: "task", taskID: created.id });
          }}
        />
      )}
    </section>
  );
}

const STATE_WORD: Record<RuntimeState, string> = { idle: "Idle", working: "Working", error: "Error", offline: "Offline" };
const STATE_TEXT: Record<RuntimeState, string> = { idle: "text-online", working: "text-busy", error: "text-danger", offline: "text-fg-3" };

type AgentStateFilter = "all" | RuntimeState;

function agentDriverPrimary(m: Member): string {
  const ag = m.agent;
  if (!ag) return "—";
  if (ag.driver_kind === "acp") return `ACP · ${ag.driver_config?.provider ?? "?"}`;
  return "Model API";
}

function agentDriverSecondary(m: Member): string {
  const cfg = m.agent?.driver_config;
  if (!cfg) return "";
  if (m.agent?.driver_kind === "builtin") return cfg.model ?? "";
  if (cfg.provider === "pi") {
    return [cfg.pi_provider, cfg.pi_model].filter(Boolean).join(" / ");
  }
  return "";
}

/** Slack/Mattermost-style agent directory table with docked filters, status breakdown, and inline DM/copy actions. */
function AgentList({
  boot,
  selectedID,
  onInspect,
  onOpenDM,
  onCopyAgent,
}: {
  boot: BootstrapData;
  selectedID?: string;
  onInspect: (id: string) => void;
  onOpenDM?: (memberID: string) => void;
  onCopyAgent?: (agent: Member) => void;
}) {
  const [q, setQ] = useState("");
  const spellCheck = useSpellCheckAttr();
  const [stateFilter, setStateFilter] = useState<AgentStateFilter>("all");
  const [group, setGroup] = useState<"none" | "computer">("none");
  const allAgents = boot.members.filter((m) => m.kind === "agent");
  const counts = {
    all: allAgents.length,
    working: allAgents.filter((m) => m.agent?.runtime_state === "working").length,
    idle: allAgents.filter((m) => m.agent?.runtime_state === "idle").length,
    offline: allAgents.filter((m) => (m.agent?.runtime_state ?? "offline") === "offline").length,
    error: allAgents.filter((m) => m.agent?.runtime_state === "error").length,
  };
  const query = q.trim().toLowerCase();
  const agents = allAgents.filter((m) => {
    const st = m.agent?.runtime_state ?? "offline";
    if (stateFilter !== "all" && st !== stateFilter) return false;
    if (!query) return true;
    const comp = boot.computers.find((c) => c.id === m.agent?.computer_id);
    return (
      m.display_name.toLowerCase().includes(query) ||
      (m.agent?.role_text ?? "").toLowerCase().includes(query) ||
      agentDriverPrimary(m).toLowerCase().includes(query) ||
      agentDriverSecondary(m).toLowerCase().includes(query) ||
      (comp?.name ?? "").toLowerCase().includes(query)
    );
  });

  const groups =
    group === "none"
      ? [{ key: "all", label: "", subtitle: "", online: undefined as boolean | undefined, items: agents }]
      : boot.computers
          .map((c) => ({
            key: c.id,
            label: c.name,
            subtitle: c.hostname,
            online: c.connection_status === "online",
            items: agents.filter((a) => a.agent?.computer_id === c.id),
          }))
          .filter((g) => g.items.length > 0);

  const filterPills: { value: AgentStateFilter; label: string; count: number; dot?: RuntimeState }[] = [
    { value: "all", label: "All", count: counts.all },
    { value: "working", label: "Working", count: counts.working, dot: "working" },
    { value: "idle", label: "Idle", count: counts.idle, dot: "idle" },
    { value: "offline", label: "Offline", count: counts.offline, dot: "offline" },
    ...(counts.error > 0 || stateFilter === "error" ? [{ value: "error" as const, label: "Error", count: counts.error, dot: "error" as const }] : []),
  ];

  return (
    <div className="@container flex flex-col">
      <div className="px-6 py-2.5 border-b border-line bg-panel flex items-center gap-2.5 flex-wrap">
        <div className="relative flex-1 min-w-[200px] max-w-[300px]">
          <Search className="size-3.5 text-fg-4 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
          <input
            value={q}
            spellCheck={spellCheck}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search agents by name, role, or model…"
            aria-label="Search agents"
            className="w-full h-8 pl-8 pr-7 rounded-[5px] border border-line-strong bg-card text-[13px] outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
          />
          {q && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={() => setQ("")}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 size-5 rounded-[4px] text-fg-4 hover:text-fg hover:bg-hover flex items-center justify-center"
            >
              <X className="size-3.5" />
            </button>
          )}
        </div>
        <div role="tablist" aria-label="Filter agents by status" className="flex items-center gap-1 flex-wrap">
          {filterPills.map((p) => {
            const active = stateFilter === p.value;
            return (
              <button
                key={p.value}
                type="button"
                role="tab"
                aria-selected={active}
                aria-label={`${p.label} ${p.count}`}
                onClick={() => setStateFilter(p.value)}
                className={`h-7 px-2.5 rounded-[4px] border text-[12.5px] inline-flex items-center gap-1.5 transition-colors ${
                  active
                    ? "border-line-strong bg-subtle text-fg font-semibold"
                    : "border-transparent text-fg-3 hover:bg-subtle/70 hover:text-fg"
                }`}
              >
                {p.dot && <StatusDot state={p.dot} size={6} ring="transparent" />}
                <span>{p.label}</span>
                <span className={`tabular font-mono text-[11.5px] ${active ? "text-fg-2" : "text-fg-4"}`}>{p.count}</span>
              </button>
            );
          })}
        </div>
        <span className="flex-1" />
        <Segmented
          value={group}
          onChange={setGroup}
          options={[
            { value: "none", label: "List" },
            { value: "computer", label: <><Monitor />By computer</> },
          ]}
        />
      </div>

      {agents.length === 0 ? (
        <div className="px-6 py-14 text-center">
          <p className="text-[14px] text-fg-2 font-medium">No agents match this filter</p>
          <p className="mt-1 text-[13px] text-fg-3">
            {q ? `No matches for “${q}”.` : `No agents are currently ${STATE_WORD[stateFilter === "all" ? "idle" : stateFilter].toLowerCase()}.`}
          </p>
          {(q || stateFilter !== "all") && (
            <div className="mt-3">
              <Button
                size="sm"
                variant="secondary"
                onClick={() => {
                  setQ("");
                  setStateFilter("all");
                }}
              >
                Reset filters
              </Button>
            </div>
          )}
        </div>
      ) : (
        <div>
          <div className="hidden @[640px]:flex h-8 px-6 bg-subtle/60 border-b border-line items-center gap-3 text-[11px] font-semibold uppercase tracking-[0.05em] text-fg-3 select-none">
            <span className="flex-1 min-w-0">Agent</span>
            <span className="w-[100px] shrink-0">Status</span>
            <span className="w-[144px] shrink-0">Driver</span>
            <span className="w-[144px] shrink-0">Computer</span>
            <span className="w-[84px] shrink-0 text-right">Actions</span>
          </div>
          {groups.map((g) => (
            <div key={g.key}>
              {g.label && (
                <div className="h-8 px-6 bg-subtle/85 border-b border-line flex items-center gap-2 text-[12px] font-semibold text-fg-2">
                  <span className={`size-2 rounded-full shrink-0 ${g.online ? "bg-online" : "bg-fg-4"}`} />
                  <span>{g.label}</span>
                  {g.subtitle && <span className="font-mono font-normal text-[11.5px] text-fg-4">{g.subtitle}</span>}
                  <span className="ml-auto font-mono font-normal text-[11.5px] text-fg-4 tabular">{g.items.length}</span>
                </div>
              )}
              <div className="divide-y divide-line border-b border-line">
                {g.items.map((m) => {
                  const ag = m.agent;
                  const st = ag?.runtime_state ?? "offline";
                  const comp = boot.computers.find((c) => c.id === ag?.computer_id);
                  const driverPrimary = agentDriverPrimary(m);
                  const driverSecondary = agentDriverSecondary(m);
                  const selected = m.id === selectedID;
                  return (
                    <div
                      key={m.id}
                      className={`relative group w-full flex items-center transition-colors ${
                        selected ? "bg-accent-soft/55" : "hover:bg-subtle/60"
                      }`}
                    >
                      {selected && <span aria-hidden className="absolute left-0 inset-y-0 w-[3px] bg-accent" />}
                      <button
                        type="button"
                        onClick={() => onInspect(m.id)}
                        aria-current={selected || undefined}
                        className="min-w-0 flex-1 pl-6 pr-2 py-3 flex items-center gap-3 text-left"
                      >
                        <MemberAvatar
                          id={m.id}
                          name={m.display_name}
                          kind="agent"
                          size="md"
                          state={st}
                          ring={selected ? "var(--accent-soft)" : "var(--panel)"}
                        />
                        <span className="min-w-0 flex-1">
                          <span className="flex items-baseline gap-2 min-w-0">
                            <span className="text-[14px] font-semibold text-fg truncate">{m.display_name}</span>
                            {st === "working" || st === "error" ? (
                              <span
                                className={`text-[12.5px] truncate ${st === "error" ? "text-danger" : "text-busy"}`}
                                title={[agentStatusText(boot, m), st === "working" ? activityLabel(ag?.activity) : ag?.last_error_detail].filter(Boolean).join(" · ")}
                              >
                                {agentStatusText(boot, m)}
                                {st === "working" && activityLabel(ag?.activity) && (
                                  <span className="font-mono text-[11.5px] opacity-80"> · {activityLabel(ag?.activity)}</span>
                                )}
                              </span>
                            ) : null}
                          </span>
                          <span className="block text-[12.5px] text-fg-3 truncate mt-0.5" title={ag?.role_text}>
                            {ag?.role_text || "No role defined."}
                          </span>
                          <span className="@[640px]:hidden mt-1 block font-mono text-[11.5px] text-fg-4 truncate">
                            {[STATE_WORD[st], driverSecondary || driverPrimary, comp?.name].filter(Boolean).join(" · ")}
                          </span>
                        </span>
                        <span className={`hidden @[640px]:flex items-center gap-1.5 w-[100px] text-[13px] font-medium shrink-0 ${STATE_TEXT[st]}`}>
                          <StatusDot state={st} size={7} ring="transparent" />
                          {STATE_WORD[st]}
                        </span>
                        <span className="hidden @[640px]:block w-[144px] shrink-0 min-w-0">
                          <span className="block font-mono text-[12.5px] text-fg-2 truncate" title={driverPrimary}>
                            {driverPrimary}
                          </span>
                          {driverSecondary && (
                            <span className="block font-mono text-[11.5px] text-fg-4 truncate" title={driverSecondary}>
                              {driverSecondary}
                            </span>
                          )}
                        </span>
                        <span className="hidden @[640px]:flex items-center gap-1.5 w-[144px] text-[13px] text-fg-2 shrink-0 min-w-0" title={comp?.hostname}>
                          <span className={`size-1.5 rounded-full shrink-0 ${comp?.connection_status === "online" ? "bg-online" : "bg-fg-4"}`} />
                          <span className="truncate">{comp?.name ?? "No computer"}</span>
                        </span>
                      </button>
                      <div className="pr-5 w-[84px] flex items-center justify-end gap-0.5 shrink-0">
                        {onOpenDM && (
                          <IconButton
                            label={`Message ${m.display_name}`}
                            onClick={() => onOpenDM(m.id)}
                            className={`transition-opacity ${selected ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"}`}
                          >
                            <MessageSquare />
                          </IconButton>
                        )}
                        {onCopyAgent && (
                          <IconButton
                            label={`Copy ${m.display_name} to new agent`}
                            onClick={() => onCopyAgent(m)}
                            className={`transition-opacity ${selected ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"}`}
                          >
                            <Copy />
                          </IconButton>
                        )}
                        <ChevronRight className="size-4 text-fg-4 opacity-0 group-hover:opacity-100 transition-opacity shrink-0 pointer-events-none" />
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function AccentPicker({ spaceSlug, value, disabled }: { spaceSlug: string; value: SpaceAccent; disabled: boolean }) {
  const qc = useQueryClient();
  const { busy, run } = useAction();
  return (
    <div role="radiogroup" aria-label="Space accent" className="flex items-center gap-2 flex-wrap">
      {SPACE_ACCENTS.map((a) => {
        const active = a.value === value;
        return (
          <button
            key={a.value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={a.label}
            title={disabled ? "Only owners and admins can change the accent" : a.label}
            disabled={disabled || busy}
            onClick={() =>
              void run(async () => {
                await api.updateSpace(spaceSlug, { accent: a.value });
                await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                await qc.invalidateQueries({ queryKey: ["me"] });
              })
            }
            className={`h-8 px-2.5 rounded-[5px] border inline-flex items-center gap-2 text-[13px] transition-colors disabled:opacity-55 disabled:cursor-not-allowed ${
              active
                ? "border-accent bg-accent-soft/60 text-fg font-semibold"
                : "border-line-strong bg-card text-fg-2 hover:bg-subtle hover:text-fg"
            }`}
          >
            <span className="size-3.5 rounded-[3px] shrink-0" style={{ background: a.swatch }} />
            <span>{a.label}</span>
          </button>
        );
      })}
    </div>
  );
}

function ThemeCard({
  value,
  active,
  label,
  caption,
  onSelect,
}: {
  value: ThemePref;
  active: boolean;
  label: string;
  caption: string;
  onSelect: () => void;
}) {
  const preview = (cls: string) => (
    <div className={`${cls} flex-1 h-full flex overflow-hidden`}>
      <div className="w-[26%] h-full border-r flex flex-col gap-1 p-1.5" style={{ background: "var(--swatch-nav)", borderColor: "var(--swatch-line)" }}>
        <span className="h-1.5 w-3/4 rounded-[1px] bg-white/30" />
        <span className="h-1 w-full rounded-[1px] bg-white/15" />
        <span className="h-1 w-2/3 rounded-[1px] bg-white/15" />
      </div>
      <div className="flex-1 h-full p-1.5 flex flex-col gap-1.5" style={{ background: "var(--swatch-canvas)" }}>
        <span className="h-1.5 w-2/5 rounded-[1px]" style={{ background: "var(--swatch-bar)" }} />
        <span className="h-1.5 w-4/5 rounded-[1px]" style={{ background: "var(--swatch-bar-2)" }} />
        <span className="h-1.5 w-3/5 rounded-[1px]" style={{ background: "var(--swatch-bar-2)" }} />
      </div>
    </div>
  );

  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      aria-label={label}
      onClick={onSelect}
      className={`text-left rounded-[6px] border p-2 transition-colors ${
        active
          ? "border-accent bg-accent-soft/45"
          : "border-line-strong bg-card hover:bg-subtle/60"
      }`}
    >
      <div className="h-14 rounded-[4px] border border-line overflow-hidden flex">
        {value === "system" ? (
          <>
            {preview("theme-swatch-light")}
            <span className="w-px bg-line shrink-0" />
            {preview("theme-swatch-dark")}
          </>
        ) : value === "light" ? (
          preview("theme-swatch-light")
        ) : (
          preview("theme-swatch-dark")
        )}
      </div>
      <div className="mt-2 px-0.5 flex items-center justify-between gap-2">
        <span className="min-w-0">
          <span className={`block text-[13px] leading-tight ${active ? "font-semibold text-fg" : "font-medium text-fg-2"}`}>{label}</span>
          <span className="block text-[11px] text-fg-4 leading-tight mt-0.5">{caption}</span>
        </span>
        <span
          aria-hidden
          className={`size-4 rounded-full border flex items-center justify-center shrink-0 ${
            active ? "border-accent bg-accent text-accent-fg" : "border-line-strong bg-card"
          }`}
        >
          {active && <Check className="size-2.5 stroke-[3]" />}
        </span>
      </div>
    </button>
  );
}

function SettingsSectionHeading({ title, body }: { title: string; body?: string }) {
  return (
    <div className="mb-4">
      <h2 className="text-[15.5px] font-semibold text-fg leading-snug">{title}</h2>
      {body && <p className="text-[13px] text-fg-3 mt-0.5 leading-relaxed">{body}</p>}
    </div>
  );
}

function ArchivedChannelsSection({
  spaceSlug,
  canGovern,
  onOpenChannel,
}: {
  spaceSlug: string;
  canGovern: boolean;
  onOpenChannel?: (conv: Conversation) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const act = useAction();
  const [busyID, setBusyID] = useState<string | null>(null);
  const archivedQ = useQuery({
    queryKey: ["archived-channels", spaceSlug],
    queryFn: () => api.listArchivedChannels(spaceSlug).then((r) => r.conversations),
  });
  const list = archivedQ.data ?? [];

  return (
    <div>
      <SettingsSectionHeading
        title="Archived channels"
        body="Read-only channels. Unarchive a channel to resume messaging and notifications."
      />
      {archivedQ.isLoading ? (
        <div className="rounded-[6px] border border-line bg-subtle/30 px-4 py-5 text-[13px] text-fg-3">Loading archived channels…</div>
      ) : list.length === 0 ? (
        <div className="rounded-[6px] border border-line bg-subtle/25 px-4 py-5 flex items-center gap-3 text-[13px] text-fg-3">
          <Archive className="size-4 text-fg-4 shrink-0" />
          <span>No archived channels in this space.</span>
        </div>
      ) : (
        <div className="rounded-[6px] border border-line bg-panel divide-y divide-line overflow-hidden">
          {list.map((c) => (
            <div key={c.id} className="px-4 py-3 flex items-center gap-3 hover:bg-subtle/50 transition-colors">
              <span className="size-8 rounded-[5px] bg-subtle border border-line text-fg-3 flex items-center justify-center shrink-0">
                {c.kind === "private" ? <Lock className="size-4" /> : <Hash className="size-4" />}
              </span>
              <button
                type="button"
                onClick={() => onOpenChannel?.(c)}
                className="min-w-0 flex-1 text-left group"
              >
                <div className="text-[13.5px] font-semibold text-fg group-hover:underline underline-offset-2 truncate">#{c.slug}</div>
                <div className="text-[12px] text-fg-3 truncate">
                  {c.kind === "private" ? "Private" : "Public"}
                  {c.archived_at ? ` · Archived ${formatDay(c.archived_at)} ${formatClock(c.archived_at)}` : ""}
                  {c.topic ? ` · ${c.topic}` : ""}
                </div>
              </button>
              <div className="flex items-center gap-1.5 shrink-0">
                {onOpenChannel && (
                  <Button size="sm" variant="ghost" onClick={() => onOpenChannel(c)}>
                    View
                  </Button>
                )}
                {canGovern && (
                  <Button
                    size="sm"
                    variant="secondary"
                    icon={<ArchiveRestore />}
                    loading={busyID === c.id && act.busy}
                    disabled={act.busy}
                    onClick={() => {
                      setBusyID(c.id);
                      void act
                        .run(async () => {
                          await api.unarchiveChannel(spaceSlug, c.id);
                          toast(`Unarchived #${c.slug}`, "success");
                          await qc.invalidateQueries({ queryKey: ["archived-channels", spaceSlug] });
                          await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                        })
                        .finally(() => setBusyID(null));
                    }}
                  >
                    Unarchive
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

type SettingsSectionID = "general" | "space" | "archived" | "account";

const SETTINGS_NAV: { id: SettingsSectionID; label: string; icon: ReactNode }[] = [
  { id: "general", label: "General", icon: <SlidersHorizontal /> },
  { id: "space", label: "Space", icon: <Layers /> },
  { id: "archived", label: "Archived channels", icon: <Archive /> },
  { id: "account", label: "Account", icon: <User /> },
];

const FONT_FAMILY_LABELS: Record<FontFamilyPref, string> = {
  inter: "Inter",
  system: "System",
  serif: "Serif",
  mono: "Mono",
};

const FONT_SIZE_LABELS: Record<FontSizePref, string> = {
  sm: "13px",
  md: "14.5px",
  lg: "16px",
  xl: "18px",
};

export function AdminPanel({
  boot,
  tab,
  onTab,
  onInspectAgent,
  selectedAgentID,
  onInspectHuman,
  selectedHumanID,
  onOpenDM,
  onNewAgent,
  onCopyAgent,
  onConnectComputer,
  onOpenArchivedChannel,
  theme,
  onTheme,
  onLogout,
}: {
  boot: BootstrapData;
  tab: AdminTab;
  onTab: (t: AdminTab) => void;
  onInspectAgent: (memberID: string) => void;
  selectedAgentID?: string;
  onInspectHuman?: (memberID: string) => void;
  selectedHumanID?: string;
  onOpenDM?: (memberID: string) => void;
  onNewAgent: () => void;
  onCopyAgent?: (agent: Member) => void;
  onConnectComputer: () => void;
  onOpenArchivedChannel?: (conv: Conversation) => void;
  theme: ThemePref;
  onTheme: (t: ThemePref) => void;
  onLogout: () => void;
}) {
  const humans = boot.members.filter((m) => m.kind === "human");
  const agents = boot.members.filter((m) => m.kind === "agent");
  const onlineComputers = boot.computers.filter((c) => c.connection_status === "online").length;
  const { templates, remove: removeTemplate } = useAgentTemplates();
  const [editingTemplate, setEditingTemplate] = useState<AgentTemplate | null>(null);
  const [peopleQuery, setPeopleQuery] = useState("");
  const [activeSettingsSection, setActiveSettingsSection] = useState<SettingsSectionID>("general");
  const settingsRefs = useRef<Partial<Record<SettingsSectionID, HTMLElement | null>>>({});
  const [sendKey, setSendKey] = useSendKey();
  const [spellCheck, setSpellCheck] = useSpellCheck();
  const [longMessages, setLongMessages] = useLongMessagesPref();
  const [fontFamily, setFontFamily] = useFontFamily();
  const [fontSize, setFontSize] = useFontSize();

  const filteredHumans = humans.filter((m) => {
    const q = peopleQuery.trim().toLowerCase();
    if (!q) return true;
    return m.display_name.toLowerCase().includes(q) || m.access_level.toLowerCase().includes(q);
  });

  function jumpToSettings(id: SettingsSectionID) {
    setActiveSettingsSection(id);
    settingsRefs.current[id]?.scrollIntoView?.({ behavior: "smooth", block: "start" });
  }

  const headerActions =
    tab === "agents" ? (
      <Button size="sm" icon={<Plus />} onClick={onNewAgent}>
        New agent
      </Button>
    ) : tab === "computers" ? (
      <Button size="sm" icon={<Plus />} onClick={onConnectComputer}>
        Connect computer
      </Button>
    ) : undefined;

  return (
    <section className="flex flex-col h-full min-h-0 bg-panel">
      <ViewHeader
        title={boot.space.name}
        icon={
          <span
            aria-hidden
            className="size-5 rounded-[4px] text-[11px] font-bold inline-flex items-center justify-center shrink-0"
            style={{ background: accentSwatch(boot.space.accent), color: accentForeground(boot.space.accent) }}
          >
            {(boot.space.name[0] ?? "S").toUpperCase()}
          </span>
        }
        actions={headerActions}
      >
        <span className="w-px h-4 bg-line shrink-0" />
        <HeaderTabs
          value={tab}
          onChange={onTab}
          options={[
            {
              value: "agents",
              label: (
                <>
                  <Bot />
                  <span>Agents</span>
                  {agents.length > 0 && <span className="font-mono text-[11.5px] text-fg-4 tabular">{agents.length}</span>}
                </>
              ),
            },
            {
              value: "computers",
              label: (
                <>
                  <Monitor />
                  <span>Computers</span>
                  {boot.computers.length > 0 && <span className="font-mono text-[11.5px] text-fg-4 tabular">{boot.computers.length}</span>}
                </>
              ),
            },
            {
              value: "people",
              label: (
                <>
                  <Users />
                  <span>People</span>
                  {humans.length > 0 && <span className="font-mono text-[11.5px] text-fg-4 tabular">{humans.length}</span>}
                </>
              ),
            },
            { value: "settings", label: "Settings" },
          ]}
        />
      </ViewHeader>

      {tab === "agents" && (
        <div className="flex-1 min-h-0 overflow-y-auto scroll-thin">
          {agents.length === 0 ? (
            <EmptyState
              icon={<Bot />}
              title="No agents yet"
              body="Create a persistent agent with its own role, memory and workspace, then DM it or invite it into channels."
              action={
                <Button icon={<Plus />} onClick={onNewAgent}>
                  Create an agent
                </Button>
              }
            />
          ) : (
            <AgentList
              boot={boot}
              selectedID={selectedAgentID}
              onInspect={onInspectAgent}
              onOpenDM={onOpenDM}
              onCopyAgent={onCopyAgent}
            />
          )}

          {templates.length > 0 && (
            <div className="@container mt-6">
              <div className="px-6 py-2.5 bg-subtle/50 border-y border-line flex items-center justify-between gap-3">
                <div className="flex items-center gap-2">
                  <Bookmark className="size-3.5 text-fg-3" />
                  <h2 className="text-[12.5px] font-semibold uppercase tracking-[0.05em] text-fg-2">Templates</h2>
                  <span className="font-mono text-[11.5px] text-fg-4 tabular">{templates.length}</span>
                </div>
                <span className="text-[12px] text-fg-3 hidden @[560px]:inline">Saved on this device for creating or updating agents</span>
              </div>
              <div className="divide-y divide-line border-b border-line">
                {templates.map((tpl) => {
                  const comp = tpl.computer_id ? boot.computers.find((c) => c.id === tpl.computer_id) : undefined;
                  return (
                    <div key={tpl.id} className="group px-6 py-2.5 flex items-center gap-3 hover:bg-subtle/60 transition-colors">
                      <span className="size-9 rounded-[5px] bg-subtle border border-line text-fg-3 flex items-center justify-center shrink-0">
                        <Bookmark className="size-4" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="text-[13.5px] font-semibold text-fg truncate">{tpl.name}</div>
                        <div className="text-[12.5px] text-fg-3 truncate">{tpl.role_text || "No role defined."}</div>
                      </div>
                      <span className="hidden @[640px]:block w-[144px] font-mono text-[12.5px] text-fg-2 truncate shrink-0" title={templateDriverSummary(tpl)}>
                        {templateDriverSummary(tpl)}
                      </span>
                      <span className="hidden @[640px]:flex items-center gap-1.5 w-[144px] text-[13px] text-fg-2 shrink-0 min-w-0">
                        {comp ? (
                          <>
                            <span className={`size-1.5 rounded-full shrink-0 ${comp.connection_status === "online" ? "bg-online" : "bg-fg-4"}`} />
                            <span className="truncate">{comp.name}</span>
                          </>
                        ) : (
                          <span className="text-fg-4 truncate">Any computer</span>
                        )}
                      </span>
                      <div className="w-[84px] flex items-center justify-end gap-0.5 shrink-0">
                        <IconButton label={`Edit template ${tpl.name}`} onClick={() => setEditingTemplate(tpl)}>
                          <Pencil />
                        </IconButton>
                        <IconButton label={`Delete template ${tpl.name}`} onClick={() => removeTemplate(tpl.id)} className="hover:text-danger">
                          <Trash2 />
                        </IconButton>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          <AgentTemplateDialog
            open={Boolean(editingTemplate)}
            template={editingTemplate}
            computers={boot.computers}
            onClose={() => setEditingTemplate(null)}
          />
        </div>
      )}

      {tab === "computers" && (
        <div className="flex-1 min-h-0 overflow-y-auto scroll-thin @container">
          <div className="px-6 py-2.5 border-b border-line bg-panel flex items-center justify-between gap-3 flex-wrap text-[13px]">
            <span className="text-fg-3">Machines that host this space&apos;s agents and local workspaces.</span>
            {boot.computers.length > 0 && (
              <span className="inline-flex items-center gap-2 text-[12.5px] text-fg-2 tabular shrink-0">
                <span className={`size-2 rounded-full ${onlineComputers > 0 ? "bg-online" : "bg-fg-4"}`} />
                <span className="font-medium">{onlineComputers} online</span>
                <span className="text-fg-4">·</span>
                <span className="text-fg-3">{boot.computers.length} total</span>
              </span>
            )}
          </div>
          {boot.computers.length === 0 ? (
            <EmptyState
              icon={<Monitor />}
              title="No computers yet"
              body="Open the Sumi desktop app and confirm it, or run one command on any machine."
              action={
                <Button icon={<Plus />} onClick={onConnectComputer}>
                  Connect a computer
                </Button>
              }
            />
          ) : (
            <div>
              <div className="hidden @[700px]:flex h-8 px-6 bg-subtle/60 border-b border-line items-center gap-3 text-[11px] font-semibold uppercase tracking-[0.05em] text-fg-3 select-none">
                <span className="flex-1 min-w-0">Computer</span>
                <span className="w-[180px] shrink-0">Host &amp; OS</span>
                <span className="w-[160px] shrink-0">Coding agents</span>
                <span className="w-[108px] shrink-0">Agents</span>
                <span className="w-[120px] shrink-0 text-right">Status</span>
              </div>
              <div className="divide-y divide-line border-b border-line">
                {boot.computers.map((c) => {
                  const hosted = agents.filter((a) => a.agent?.computer_id === c.id);
                  const online = c.connection_status === "online";
                  const ownerLabel =
                    c.owner_member_id === boot.current_member.id
                      ? "Yours"
                      : `${boot.members.find((m) => m.id === c.owner_member_id)?.display_name ?? "Former member"}'s`;
                  return (
                    <div key={c.id} className="px-6 py-3 flex items-center gap-3 hover:bg-subtle/60 transition-colors">
                      <span className="size-9 rounded-[5px] bg-subtle border border-line text-fg-2 flex items-center justify-center shrink-0">
                        <Monitor className="size-4" />
                      </span>
                      <div className="min-w-0 flex-1">
                        <div className="text-[14px] font-semibold text-fg truncate">{c.name}</div>
                        <div className="text-[12.5px] text-fg-3 truncate">
                          {ownerLabel}
                          <span className="@[700px]:hidden">
                            {" "}
                            · {c.hostname} · {c.os === "macos" ? "macOS" : "Linux"}
                          </span>
                        </div>
                      </div>
                      <div className="hidden @[700px]:block w-[180px] shrink-0 min-w-0">
                        <div className="font-mono text-[12.5px] text-fg-2 truncate" title={c.hostname}>
                          {c.hostname}
                        </div>
                        <div className="text-[12px] text-fg-4">{c.os === "macos" ? "macOS" : "Linux"}</div>
                      </div>
                      <div className="hidden @[700px]:flex items-center gap-1 flex-wrap w-[160px] shrink-0">
                        {c.acp_providers && c.acp_providers.length > 0 ? (
                          c.acp_providers.map((p) => (
                            <span
                              key={p}
                              className="h-5 px-1.5 rounded-[4px] bg-subtle border border-line font-mono text-[11.5px] text-fg-2 inline-flex items-center"
                            >
                              {p}
                            </span>
                          ))
                        ) : (
                          <span className="text-[12.5px] text-fg-4">—</span>
                        )}
                      </div>
                      <div className="hidden @[700px]:flex items-center gap-1.5 w-[108px] shrink-0">
                        {hosted.length > 0 ? (
                          <>
                            <div className="flex -space-x-1.5">
                              {hosted.slice(0, 4).map((a) => (
                                <button
                                  key={a.id}
                                  type="button"
                                  title={a.display_name}
                                  onClick={() => onInspectAgent(a.id)}
                                  className="rounded-[4px] ring-2 ring-[var(--panel)] hover:z-10 transition-transform hover:scale-105"
                                >
                                  <MemberAvatar id={a.id} name={a.display_name} kind="agent" size="xs" />
                                </button>
                              ))}
                            </div>
                            <span className="tabular font-mono text-[11.5px] text-fg-4">{hosted.length}</span>
                          </>
                        ) : (
                          <span className="text-[12.5px] text-fg-4">None</span>
                        )}
                      </div>
                      <div className="w-[120px] flex justify-end shrink-0">
                        <Badge tone={online ? "online" : "neutral"}>
                          <span className={`size-1.5 rounded-full ${online ? "bg-online" : "bg-fg-4"}`} />
                          {online ? "Online" : c.last_seen_at ? `Seen ${formatDay(c.last_seen_at)} ${formatClock(c.last_seen_at)}` : "Offline"}
                        </Badge>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          )}
        </div>
      )}

      {tab === "people" && (
        <div className="flex-1 min-h-0 overflow-y-auto scroll-thin @container">
          <div className="px-6 py-2.5 border-b border-line bg-panel flex items-center justify-between gap-3 flex-wrap">
            {humans.length > 1 ? (
              <div className="relative flex-1 min-w-[200px] max-w-[300px]">
                <Search className="size-3.5 text-fg-4 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
                <input
                  value={peopleQuery}
                  spellCheck={spellCheck === "off" ? false : undefined}
                  onChange={(e) => setPeopleQuery(e.target.value)}
                  placeholder="Search people…"
                  aria-label="Search people"
                  className="w-full h-8 pl-8 pr-7 rounded-[5px] border border-line-strong bg-card text-[13px] outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
                />
                {peopleQuery && (
                  <button
                    type="button"
                    aria-label="Clear search"
                    onClick={() => setPeopleQuery("")}
                    className="absolute right-1.5 top-1/2 -translate-y-1/2 size-5 rounded-[4px] text-fg-4 hover:text-fg hover:bg-hover flex items-center justify-center"
                  >
                    <X className="size-3.5" />
                  </button>
                )}
              </div>
            ) : (
              <span className="text-[13px] text-fg-3">Humans collaborating in {boot.space.name}.</span>
            )}
            <span className="text-[12.5px] text-fg-3 tabular">
              {humans.length} {humans.length === 1 ? "person" : "people"}
            </span>
          </div>

          <div className="hidden @[560px]:flex h-8 px-6 bg-subtle/60 border-b border-line items-center gap-3 text-[11px] font-semibold uppercase tracking-[0.05em] text-fg-3 select-none">
            <span className="flex-1 min-w-0">Member</span>
            <span className="w-[100px] shrink-0">Access</span>
            <span className="w-[120px] shrink-0">Computers</span>
            <span className="w-[64px] shrink-0" />
          </div>

          {filteredHumans.length === 0 ? (
            <div className="px-6 py-12 text-center text-[13.5px] text-fg-3">No people match “{peopleQuery}”.</div>
          ) : (
            <div className="divide-y divide-line border-b border-line">
              {filteredHumans.map((m) => {
                const selected = m.id === selectedHumanID;
                const isSelf = m.id === boot.current_member.id;
                const owned = boot.computers.filter((c) => c.owner_member_id === m.id);
                const ownedOnline = owned.filter((c) => c.connection_status === "online").length;
                return (
                  <div
                    key={m.id}
                    className={`relative group w-full flex items-center transition-colors ${
                      selected ? "bg-accent-soft/55" : "hover:bg-subtle/60"
                    }`}
                  >
                    {selected && <span aria-hidden className="absolute left-0 inset-y-0 w-[3px] bg-accent" />}
                    <button
                      type="button"
                      onClick={() => onInspectHuman?.(m.id)}
                      aria-current={selected || undefined}
                      className="min-w-0 flex-1 pl-6 pr-2 py-2.5 flex items-center gap-3 text-left"
                    >
                      <MemberAvatar id={m.id} name={m.display_name} kind="human" size="md" />
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-2 min-w-0">
                          <span className="text-[14px] font-semibold text-fg truncate">{m.display_name}</span>
                          {isSelf && <Badge tone="accent">You</Badge>}
                        </span>
                        <span className="block font-mono text-[12px] text-fg-4 truncate">@{m.display_name}</span>
                      </span>
                      <span className="hidden @[560px]:flex items-center w-[100px] shrink-0">
                        <Badge className="capitalize">{m.access_level}</Badge>
                      </span>
                      <span className="hidden @[560px]:flex items-center gap-1.5 w-[120px] text-[12.5px] text-fg-3 shrink-0">
                        {owned.length > 0 ? (
                          <>
                            <span className={`size-1.5 rounded-full shrink-0 ${ownedOnline > 0 ? "bg-online" : "bg-fg-4"}`} />
                            <span className="tabular">{owned.length} {owned.length === 1 ? "machine" : "machines"}</span>
                          </>
                        ) : (
                          <span className="text-fg-4">—</span>
                        )}
                      </span>
                    </button>
                    <div className="pr-5 w-[64px] flex items-center justify-end gap-0.5 shrink-0">
                      {!isSelf && onOpenDM && (
                        <IconButton
                          label={`Message ${m.display_name}`}
                          onClick={() => onOpenDM(m.id)}
                          className={`transition-opacity ${selected ? "opacity-100" : "opacity-0 group-hover:opacity-100 group-focus-within:opacity-100"}`}
                        >
                          <MessageSquare />
                        </IconButton>
                      )}
                      <ChevronRight className="size-4 text-fg-4 opacity-0 group-hover:opacity-100 transition-opacity shrink-0 pointer-events-none" />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {tab === "settings" && (
        <div className="@container flex-1 min-h-0 flex">
          <nav
            aria-label="Settings sections"
            className="hidden @[760px]:flex flex-col w-[212px] shrink-0 border-r border-line bg-subtle/25 py-4 px-3 gap-0.5 select-none"
          >
            {SETTINGS_NAV.map((item) => {
              const active = activeSettingsSection === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => jumpToSettings(item.id)}
                  className={`relative h-8 px-2.5 rounded-[5px] flex items-center gap-2.5 text-[13.5px] text-left transition-colors [&>svg]:size-4 ${
                    active
                      ? "bg-hover text-fg font-semibold"
                      : "text-fg-2 hover:bg-subtle hover:text-fg font-medium"
                  }`}
                >
                  {active && <span aria-hidden className="absolute left-0 top-1.5 bottom-1.5 w-[2.5px] rounded-r-[2px] bg-accent" />}
                  <span className={active ? "text-fg" : "text-fg-3"}>{item.icon}</span>
                  <span className="truncate">{item.label}</span>
                </button>
              );
            })}
          </nav>

          <div className="flex-1 min-w-0 overflow-y-auto scroll-thin">
            <div className="max-w-[740px] px-7 py-6 divide-y divide-line">
              <section
                ref={(el) => {
                  settingsRefs.current.general = el;
                }}
                className="pb-7 scroll-mt-6"
              >
                <SettingsSectionHeading
                  title="General"
                  body="Appearance, typography, and message preferences on this device."
                />

                <div className="pb-6">
                  <div className="mb-3">
                    <div className="text-[14px] font-medium text-fg">Theme</div>
                    <div className="text-[12.5px] text-fg-3 mt-0.5">
                      Choose Light or Dark mode, or follow your system appearance automatically.
                    </div>
                  </div>
                  <div role="radiogroup" aria-label="Theme" className="grid grid-cols-3 gap-3 max-w-[460px]">
                    <ThemeCard value="system" active={theme === "system"} label="System" caption="Follow OS" onSelect={() => onTheme("system")} />
                    <ThemeCard value="light" active={theme === "light"} label="Light" caption="Always light" onSelect={() => onTheme("light")} />
                    <ThemeCard value="dark" active={theme === "dark"} label="Dark" caption="Always dark" onSelect={() => onTheme("dark")} />
                  </div>
                </div>

                <div className="divide-y divide-line border-t border-line">
                  <div className="py-4 flex items-center justify-between gap-4 flex-wrap">
                    <div className="min-w-0">
                      <div className="text-[14px] font-medium text-fg">Font family</div>
                      <div className="text-[12.5px] text-fg-3 mt-0.5">
                        Typeface used across the workspace and messages.
                      </div>
                    </div>
                    <Segmented
                      ariaLabel="Font family"
                      value={fontFamily}
                      onChange={setFontFamily}
                      options={[
                        { value: "inter", label: "Inter" },
                        { value: "system", label: "System" },
                        { value: "serif", label: "Serif" },
                        { value: "mono", label: "Mono" },
                      ]}
                    />
                  </div>

                  <div className="py-4">
                    <div className="flex items-center justify-between gap-4 flex-wrap">
                      <div className="min-w-0">
                        <div className="text-[14px] font-medium text-fg">Text size</div>
                        <div className="text-[12.5px] text-fg-3 mt-0.5">
                          Reading size for messages, threads, and markdown previews.
                        </div>
                      </div>
                      <Segmented
                        ariaLabel="Text size"
                        value={fontSize}
                        onChange={setFontSize}
                        options={[
                          { value: "sm", label: "Small" },
                          { value: "md", label: "Default" },
                          { value: "lg", label: "Large" },
                          { value: "xl", label: "XL" },
                        ]}
                      />
                    </div>
                    <div className="mt-3 rounded-[6px] border border-line bg-subtle/30 px-3.5 py-2.5">
                      <div className="flex items-center justify-between gap-2 text-[11px] font-medium uppercase tracking-[0.05em] text-fg-4">
                        <span>Preview</span>
                        <span className="font-mono normal-case tracking-normal">
                          {FONT_FAMILY_LABELS[fontFamily]} · {FONT_SIZE_LABELS[fontSize]}
                        </span>
                      </div>
                      <div className="mt-1.5 text-[length:var(--msg-font-size)] leading-[var(--msg-line-height)] text-fg md">
                        <p>
                          Shipped the update to <code>compaction-fit.mjs</code> — ready for review when you have a moment.
                        </p>
                      </div>
                    </div>
                  </div>

                  <div className="py-4 flex items-center justify-between gap-4 flex-wrap">
                    <div className="min-w-0">
                      <div className="text-[14px] font-medium text-fg">Long messages</div>
                      <div className="text-[12.5px] text-fg-3 mt-0.5">
                        {longMessages === "expanded"
                          ? "Show full messages by default. Click Show less on tall messages to fold them."
                          : "Fold tall messages by default. Click Show more to expand them."}
                      </div>
                    </div>
                    <Segmented
                      ariaLabel="Long messages"
                      value={longMessages}
                      onChange={setLongMessages}
                      options={[
                        { value: "expanded", label: "Expanded" },
                        { value: "collapsed", label: "Collapsed" },
                      ]}
                    />
                  </div>

                  <div className="py-4 flex items-center justify-between gap-4 flex-wrap">
                    <div className="min-w-0">
                      <div className="text-[14px] font-medium text-fg">Send message shortcut</div>
                      <div className="text-[12.5px] text-fg-3 mt-0.5">
                        {sendKey === "enter"
                          ? "Press Enter to send, Shift + Enter for a new line."
                          : `Press ${isMacPlatform() ? "⌘ + Enter" : "Ctrl + Enter"} to send, Enter for a new line.`}
                      </div>
                    </div>
                    <Segmented
                      ariaLabel="Send message shortcut"
                      value={sendKey}
                      onChange={setSendKey}
                      options={[
                        { value: "enter", label: "Enter" },
                        { value: "mod_enter", label: isMacPlatform() ? "⌘ + Enter" : "Ctrl + Enter" },
                      ]}
                    />
                  </div>

                  <div className="pt-4 flex items-center justify-between gap-4 flex-wrap">
                    <div className="min-w-0">
                      <div className="text-[14px] font-medium text-fg">Spell check</div>
                      <div className="text-[12.5px] text-fg-3 mt-0.5">
                        {spellCheck === "on"
                          ? "Highlight spelling mistakes in message composers and text fields."
                          : "Disable spell checking and red underlines in text inputs."}
                      </div>
                    </div>
                    <Segmented
                      ariaLabel="Spell check"
                      value={spellCheck}
                      onChange={setSpellCheck}
                      options={[
                        { value: "on", label: "On" },
                        { value: "off", label: "Off" },
                      ]}
                    />
                  </div>
                </div>
              </section>

              <section
                ref={(el) => {
                  settingsRefs.current.space = el;
                }}
                className="py-7 scroll-mt-6"
              >
                <SettingsSectionHeading
                  title="Space"
                  body="Workspace identity and accent color shared by everyone in this space."
                />
                <div className="divide-y divide-line border-y border-line">
                  <div className="py-3 flex items-center gap-4">
                    <span className="w-36 shrink-0 text-[13px] text-fg-3">Name</span>
                    <span className="flex items-center gap-2 text-[14px] font-semibold text-fg min-w-0">
                      <span
                        aria-hidden
                        className="size-5 rounded-[4px] text-[11px] font-bold inline-flex items-center justify-center shrink-0"
                        style={{ background: accentSwatch(boot.space.accent), color: accentForeground(boot.space.accent) }}
                      >
                        {(boot.space.name[0] ?? "S").toUpperCase()}
                      </span>
                      <span className="truncate">{boot.space.name}</span>
                    </span>
                  </div>
                  <div className="py-3 flex items-center gap-4">
                    <span className="w-36 shrink-0 text-[13px] text-fg-3">Address</span>
                    <span className="font-mono text-[13px] text-fg">/{boot.space.slug}</span>
                  </div>
                  <div className="py-3 flex items-center gap-4">
                    <span className="w-36 shrink-0 text-[13px] text-fg-3">Your role</span>
                    <Badge className="capitalize">{boot.current_member.access_level}</Badge>
                  </div>
                  <div className="py-3.5 flex items-start gap-4 flex-wrap sm:flex-nowrap">
                    <div className="w-36 shrink-0 pt-1">
                      <div className="text-[13px] text-fg-3">Accent</div>
                      {boot.current_member.access_level === "member" && (
                        <div className="text-[11.5px] text-fg-4 mt-0.5">Admins only</div>
                      )}
                    </div>
                    <div className="flex-1 min-w-0">
                      <AccentPicker
                        spaceSlug={boot.space.slug}
                        value={boot.space.accent}
                        disabled={boot.current_member.access_level === "member"}
                      />
                    </div>
                  </div>
                </div>
              </section>

              <section
                ref={(el) => {
                  settingsRefs.current.archived = el;
                }}
                className="py-7 scroll-mt-6"
              >
                <ArchivedChannelsSection
                  spaceSlug={boot.space.slug}
                  canGovern={boot.current_member.access_level !== "member"}
                  onOpenChannel={onOpenArchivedChannel}
                />
              </section>

              <section
                ref={(el) => {
                  settingsRefs.current.account = el;
                }}
                className="pt-7 scroll-mt-6"
              >
                <SettingsSectionHeading title="Account" body="Your current session in this space." />
                <div className="rounded-[6px] border border-line bg-subtle/25 px-4 py-3 flex items-center justify-between gap-4 flex-wrap">
                  <div className="flex items-center gap-3 min-w-0">
                    <MemberAvatar id={boot.current_member.id} name={boot.current_member.display_name} kind="human" size="md" />
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-[14px] font-semibold text-fg truncate">{boot.current_member.display_name}</span>
                        <Badge className="capitalize">{boot.current_member.access_level}</Badge>
                      </div>
                      <div className="font-mono text-[12px] text-fg-4 truncate">@{boot.current_member.display_name}</div>
                    </div>
                  </div>
                  <Button variant="danger" size="sm" icon={<LogOut />} onClick={onLogout}>
                    Sign out
                  </Button>
                </div>
              </section>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
