import { useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AtSign,
  Bot,
  CheckCircle2,
  ChevronRight,
  CircleDot,
  Hash,
  Inbox,
  LogOut,
  MessageSquare,
  Monitor,
  Moon,
  Plus,
  Search,
  Sun,
  SunMoon,
  Users,
} from "lucide-react";
import { SPACE_ACCENTS, type SpaceAccent } from "../accent";
import { api, formatClock, formatDay, type BootstrapData, type InboxItem, type RuntimeState, type Task, type TaskStatus } from "../api";
import type { ThemePref } from "../theme";
import type { DetailTarget } from "./DetailPane";
import { agentStatusText, MemberAvatar } from "./Avatar";
import { InboxSkeleton, RowListSkeleton } from "./Skeletons";
import { Badge, Button, EmptyState, Segmented, TaskStatusIcon, taskStatusLabel, useAction } from "./ui";

export type InboxFilter = "needs_me" | "unread" | "all";
export type TaskFilter = "active" | "mine" | "done";
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
    <header className="h-[52px] px-6 border-b border-line flex items-center gap-4 shrink-0 bg-panel">
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
  const { run } = useAction();
  const [leaving, setLeaving] = useState<Set<string>>(new Set());
  const inboxQ = useQuery({
    queryKey: ["inbox", spaceSlug, filter],
    queryFn: () => api.listInbox(spaceSlug, filter).then((r) => r.items),
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
  return (
    <section className="flex flex-col h-full min-h-0 bg-bg">
      <ViewHeader title="Inbox" icon={<Inbox />}>
        <Segmented
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
        ) : items.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 />}
            title="You're all caught up"
            body={filter === "needs_me" ? "DMs, @mentions, followed threads and Tasks assigned to you land here." : "Nothing unread right now."}
          />
        ) : (
          <div className="px-6 py-4 space-y-2 max-w-[920px] animate-fade-in">
            {items.map((it) => {
              const gone = leaving.has(it.conversation_id);
              const reason = REASON[it.reason] ?? { label: it.reason, icon: <Hash /> };
              return (
                <div
                  key={it.conversation_id}
                  className={`group rounded-[12px] border bg-card px-4 py-3 flex items-center gap-3.5 transition-[opacity,transform,box-shadow,border-color] duration-200 hover:shadow-md ${
                    gone ? "opacity-0 translate-x-3 pointer-events-none" : ""
                  } ${it.needs_attention ? "border-warm/30" : "border-line"}`}
                >
                  <span
                    className={`size-9 rounded-[10px] flex items-center justify-center shrink-0 [&>svg]:size-4 ${
                      it.needs_attention ? "bg-warm-soft text-warm" : "bg-subtle text-fg-3"
                    }`}
                  >
                    {reason.icon}
                  </span>
                  <button className="min-w-0 flex-1 text-left" onClick={() => open(it)}>
                    <div className="flex items-center gap-2 text-[13px]">
                      <span className={`font-semibold ${it.needs_attention ? "text-warm" : "text-fg-2"}`}>{reason.label}</span>
                      <span className="text-fg-4">·</span>
                      <span className="text-fg-2 font-medium truncate">{it.title}</span>
                      {it.unread_count > 1 && <Badge>{it.unread_count} new</Badge>}
                    </div>
                    <p className="mt-0.5 text-[14px] text-fg truncate">
                      {it.latest_author_name && <span className="font-medium">{it.latest_author_name}: </span>}
                      <span className="text-fg-2 font-body">{it.latest_preview}</span>
                    </p>
                  </button>
                  <span className="tabular text-[12.5px] text-fg-3 shrink-0 w-12 text-right">{formatClock(it.latest_at)}</span>
                  <div className="flex items-center gap-1 shrink-0">
                    {(it.kind === "thread" || it.kind === "task") && it.following && (
                      <Button variant="ghost" size="sm" className="opacity-0 group-hover:opacity-100" onClick={() => dismiss(it, () => api.setFollowing(spaceSlug, it.conversation_id, false))}>
                        Unfollow
                      </Button>
                    )}
                    {(it.kind === "public" || it.kind === "private") && it.notify_level !== "mute" && (
                      <Button variant="ghost" size="sm" className="opacity-0 group-hover:opacity-100" onClick={() => dismiss(it, () => api.setNotifyLevel(spaceSlug, it.conversation_id, "mute"))}>
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
    <button onClick={onOpen} className="w-full h-12 px-4 hover:bg-subtle flex items-center gap-4 text-left transition-colors">
      <TaskStatusIcon status={t.status} />
      <span className="tabular text-[13px] text-fg-3 w-10 shrink-0">#{t.number}</span>
      <span className="text-[14px] font-medium text-fg truncate flex-1">{t.title}</span>
      {t.message_count > 0 && (
        <span className="inline-flex items-center gap-1.5 text-[13px] text-fg-3 shrink-0">
          <MessageSquare className="size-3.5" />
          <span className="tabular">{t.message_count}</span>
        </span>
      )}
      <span className="w-28 text-[13px] text-fg-3 truncate shrink-0">{t.channel_slug ? `#${t.channel_slug}` : "DM"}</span>
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

export function TasksView({
  spaceSlug,
  filter,
  onFilter,
  onOpenDetail,
}: {
  spaceSlug: string;
  filter: TaskFilter;
  onFilter: (f: TaskFilter) => void;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const tasksQ = useQuery({
    queryKey: ["tasks", spaceSlug, filter],
    queryFn: () => api.listTasks(spaceSlug, filter === "done" ? "done" : "active", filter === "mine" ? "mine" : "").then((r) => r.tasks),
  });
  const tasks = tasksQ.data ?? [];
  const groups = STATUS_ORDER.map((s) => ({ status: s, tasks: tasks.filter((t) => t.status === s) })).filter((g) => g.tasks.length > 0);

  return (
    <section className="flex flex-col h-full min-h-0 bg-bg">
      <ViewHeader title="Tasks" icon={<CheckCircle2 />}>
        <Segmented
          value={filter}
          onChange={onFilter}
          options={[
            { value: "active", label: "Active" },
            { value: "mine", label: "Assigned to me" },
            { value: "done", label: "Done" },
          ]}
        />
      </ViewHeader>
      <div className="flex-1 overflow-y-auto scroll-thin">
        {tasksQ.isLoading ? (
          <RowListSkeleton />
        ) : groups.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 />}
            title={filter === "done" ? "No finished tasks yet" : filter === "mine" ? "Nothing assigned to you" : "No active tasks"}
            body="Hover any message in a channel and choose Task to turn it into tracked work."
          />
        ) : (
          <div className="px-6 py-5 space-y-5 animate-fade-in">
            {groups.map((g) => (
              <div key={g.status}>
                <div className="mb-2 px-1 flex items-center gap-2 text-[13px] font-semibold text-fg-2">
                  <TaskStatusIcon status={g.status} size={14} />
                  {taskStatusLabel(g.status)}
                  <span className="tabular font-normal text-fg-4">{g.tasks.length}</span>
                </div>
                <div className="rounded-[12px] border border-line bg-card divide-y divide-line overflow-hidden">
                  {g.tasks.map((t) => (
                    <TaskRow key={t.id} t={t} onOpen={() => onOpenDetail({ kind: "task", taskID: t.id })} />
                  ))}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  );
}

const STATE_WORD: Record<RuntimeState, string> = { idle: "Idle", working: "Working", error: "Error", offline: "Offline" };
const STATE_DOT: Record<RuntimeState, string> = { idle: "bg-fg-4", working: "bg-work animate-breathe", error: "bg-danger", offline: "border border-fg-4" };

/** Raft-style agent table: identity on the left, aligned status / runtime / computer columns on the right. */
function AgentList({ boot, selectedID, onInspect }: { boot: BootstrapData; selectedID?: string; onInspect: (id: string) => void }) {
  const [q, setQ] = useState("");
  const [group, setGroup] = useState<"none" | "computer">("none");
  const query = q.trim().toLowerCase();
  const agents = boot.members
    .filter((m) => m.kind === "agent")
    .filter((m) => !query || m.display_name.toLowerCase().includes(query) || (m.agent?.role_text ?? "").toLowerCase().includes(query));
  const groups =
    group === "none"
      ? [{ key: "all", label: "", items: agents }]
      : boot.computers
          .map((c) => ({ key: c.id, label: c.name, items: agents.filter((a) => a.agent?.computer_id === c.id) }))
          .filter((g) => g.items.length > 0);
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <div className="relative flex-1 max-w-[320px]">
          <Search className="size-4 text-fg-4 absolute left-2.5 top-1/2 -translate-y-1/2" />
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search agents"
            aria-label="Search agents"
            className="w-full h-8 pl-8 pr-3 rounded-[8px] border border-line-strong bg-card text-[13.5px] outline-none placeholder:text-fg-4 focus:border-accent focus:shadow-[var(--ring)]"
          />
        </div>
        <Segmented
          value={group}
          onChange={setGroup}
          options={[
            { value: "none", label: "All" },
            { value: "computer", label: <><Monitor />By computer</> },
          ]}
        />
      </div>
      {agents.length === 0 ? (
        <p className="py-8 text-center text-[13.5px] text-fg-3">No agents match “{q}”.</p>
      ) : (
        groups.map((g) => (
          <div key={g.key}>
            {g.label && (
              <div className="px-1 pb-1.5 pt-1 flex items-center gap-2 text-[12px] font-semibold text-fg-3">
                {g.label} <span className="font-mono text-fg-4">{g.items.length}</span>
              </div>
            )}
            <div className="@container rounded-[12px] border border-line bg-card divide-y divide-line overflow-hidden">
              {g.items.map((m) => {
                const ag = m.agent;
                const st = ag?.runtime_state ?? "offline";
                const comp = boot.computers.find((c) => c.id === ag?.computer_id);
                const runtime = ag?.driver_kind === "acp" ? `ACP · ${ag.driver_config?.provider ?? "?"}` : "Model API";
                const model = ag?.driver_kind === "builtin" ? ag.driver_config?.model ?? "" : "";
                const selected = m.id === selectedID;
                return (
                  <button
                    key={m.id}
                    onClick={() => onInspect(m.id)}
                    aria-current={selected || undefined}
                    className={`group w-full px-4 py-2.5 flex items-center gap-3 text-left transition-colors ${selected ? "bg-accent-soft" : "hover:bg-subtle"}`}
                  >
                    <MemberAvatar id={m.id} name={m.display_name} kind="agent" size="md" state={st} ring={selected ? "var(--accent-soft)" : "var(--card)"} />
                    <span className="min-w-0 flex-1">
                      <span className="flex items-baseline gap-2 min-w-0">
                        <span className="text-[14px] font-semibold text-fg truncate">{m.display_name}</span>
                        {st === "working" || st === "error" ? (
                          <span className={`text-[12.5px] truncate ${st === "error" ? "text-danger" : "text-work"}`} title={agentStatusText(boot, m)}>
                            {agentStatusText(boot, m)}
                          </span>
                        ) : null}
                      </span>
                      <span className="block text-[13px] text-fg-3 truncate font-body" title={ag?.role_text}>
                        {ag?.role_text || "No role defined."}
                      </span>
                      <span className="@[640px]:hidden block font-mono text-[12px] text-fg-4 truncate">
                        {[STATE_WORD[st], runtime, model, comp?.name].filter(Boolean).join(" · ")}
                      </span>
                    </span>
                    <span className="hidden @[640px]:flex items-center gap-1.5 w-[84px] text-[13px] text-fg-2 shrink-0">
                      <span className={`size-2 rounded-full shrink-0 ${STATE_DOT[st]}`} />
                      {STATE_WORD[st]}
                    </span>
                    <span className="hidden @[640px]:block w-[104px] font-mono text-[12.5px] text-fg-3 truncate shrink-0" title={model || runtime}>
                      {model || runtime}
                    </span>
                    <span className="hidden @[640px]:flex items-center gap-1.5 w-[136px] text-[13px] text-fg-3 shrink-0 min-w-0" title={comp?.hostname}>
                      <span className={`size-1.5 rounded-full shrink-0 ${comp?.connection_status === "online" ? "bg-work" : "bg-fg-4"}`} />
                      <span className="truncate">{comp?.name ?? "No computer"}</span>
                    </span>
                    <ChevronRight className="size-4 text-fg-4 opacity-0 group-hover:opacity-100 transition-opacity shrink-0" />
                  </button>
                );
              })}
            </div>
          </div>
        ))
      )}
    </div>
  );
}

function AccentPicker({ spaceSlug, value, disabled }: { spaceSlug: string; value: SpaceAccent; disabled: boolean }) {
  const qc = useQueryClient();
  const { busy, run } = useAction();
  return (
    <div className="mb-3 px-4 h-12 rounded-[14px] border border-line bg-card flex items-center text-[14px]">
      <span className="w-40 text-fg-3">Accent</span>
      <div role="radiogroup" aria-label="Space accent" className="flex items-center gap-2">
        {SPACE_ACCENTS.map((a) => (
          <button
            key={a.value}
            role="radio"
            aria-checked={a.value === value}
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
            className={`size-6 rounded-full transition-transform disabled:cursor-not-allowed ${a.value === value ? "ring-2 ring-offset-2 ring-offset-[var(--card)] scale-105" : "hover:scale-110"}`}
            style={{ background: a.swatch, ["--tw-ring-color" as string]: a.swatch }}
          />
        ))}
      </div>
      <span className="ml-3 text-[13px] text-fg-3">{SPACE_ACCENTS.find((a) => a.value === value)?.label}</span>
    </div>
  );
}

function SectionTitle({ title, body, action }: { title: string; body?: string; action?: ReactNode }) {
  return (
    <div className="flex items-end justify-between gap-4 mb-3">
      <div>
        <h2 className="font-display text-[19px] font-semibold text-fg">{title}</h2>
        {body && <p className="text-[13.5px] text-fg-3 mt-0.5">{body}</p>}
      </div>
      {action}
    </div>
  );
}

export function AdminPanel({
  boot,
  tab,
  onTab,
  onInspectAgent,
  selectedAgentID,
  onNewAgent,
  onConnectComputer,
  theme,
  onTheme,
  onLogout,
}: {
  boot: BootstrapData;
  tab: AdminTab;
  onTab: (t: AdminTab) => void;
  onInspectAgent: (memberID: string) => void;
  selectedAgentID?: string;
  onNewAgent: () => void;
  onConnectComputer: () => void;
  theme: ThemePref;
  onTheme: (t: ThemePref) => void;
  onLogout: () => void;
}) {
  const humans = boot.members.filter((m) => m.kind === "human");
  const agents = boot.members.filter((m) => m.kind === "agent");

  return (
    <section className="flex flex-col h-full min-h-0 bg-bg">
      <ViewHeader title={boot.space.name}>
        <Segmented
          value={tab}
          onChange={onTab}
          options={[
            { value: "agents", label: <><Bot />Agents</> },
            { value: "computers", label: <><Monitor />Computers</> },
            { value: "people", label: <><Users />People</> },
            { value: "settings", label: "Settings" },
          ]}
        />
      </ViewHeader>
      <div key={tab} className="flex-1 overflow-y-auto scroll-thin animate-fade-in">
        <div className="px-6 py-6 max-w-[960px]">
          {tab === "agents" && (
            <>
              <SectionTitle
                title="Agents"
                body="Persistent teammates. Select one to see its runtime, driver and permissions."
                action={
                  <Button icon={<Plus />} onClick={onNewAgent}>
                    New agent
                  </Button>
                }
              />
              {agents.length === 0 ? (
                <div className="rounded-[14px] border border-dashed border-line-strong bg-card">
                  <EmptyState icon={<Bot />} title="No agents yet" body="Create an agent and DM it like any teammate." action={<Button onClick={onNewAgent}>Create an agent</Button>} />
                </div>
              ) : (
                <AgentList boot={boot} selectedID={selectedAgentID} onInspect={onInspectAgent} />
              )}
            </>
          )}

          {tab === "computers" && (
            <>
              <SectionTitle
                title="Computers"
                body="Machines that host this space's agents."
                action={
                  <Button icon={<Plus />} onClick={onConnectComputer}>
                    Connect computer
                  </Button>
                }
              />
              {boot.computers.length === 0 ? (
                <div className="rounded-[14px] border border-dashed border-line-strong bg-card">
                  <EmptyState
                    icon={<Monitor />}
                    title="No computers yet"
                    body="Open the Sumi desktop app and confirm it, or run one command on any machine."
                    action={<Button onClick={onConnectComputer}>Connect a computer</Button>}
                  />
                </div>
              ) : (
                <div className="rounded-[14px] border border-line bg-card divide-y divide-line overflow-hidden">
                  {boot.computers.map((c) => {
                    const hosted = agents.filter((a) => a.agent?.computer_id === c.id);
                    const online = c.connection_status === "online";
                    return (
                      <div key={c.id} className="px-4 py-3.5 flex items-center gap-3.5">
                        <span className="size-10 rounded-[11px] bg-subtle text-fg-2 flex items-center justify-center shrink-0">
                          <Monitor className="size-5" />
                        </span>
                        <div className="min-w-0 flex-1">
                          <div className="text-[14.5px] font-semibold text-fg truncate">{c.name}</div>
                          <div className="text-[13px] text-fg-3 truncate">
                            {c.hostname} · {c.os === "macos" ? "macOS" : "Linux"}
                            {c.acp_providers && c.acp_providers.length > 0 && ` · ACP: ${c.acp_providers.join(", ")}`}
                          </div>
                        </div>
                        <div className="flex -space-x-1.5">
                          {hosted.slice(0, 5).map((a) => (
                            <span key={a.id} className="rounded-[7px] ring-2 ring-[var(--card)]">
                              <MemberAvatar id={a.id} name={a.display_name} kind="agent" size="sm" />
                            </span>
                          ))}
                        </div>
                        <Badge tone={online ? "work" : "neutral"}>
                          <span className={`size-1.5 rounded-full ${online ? "bg-work" : "bg-fg-4"}`} />
                          {online ? "Online" : c.last_seen_at ? `Seen ${formatDay(c.last_seen_at)} ${formatClock(c.last_seen_at)}` : "Offline"}
                        </Badge>
                      </div>
                    );
                  })}
                </div>
              )}
            </>
          )}

          {tab === "people" && (
            <>
              <SectionTitle title="People" body="Humans in this space." />
              <div className="rounded-[14px] border border-line bg-card divide-y divide-line overflow-hidden">
                {humans.map((m) => (
                  <div key={m.id} className="px-4 h-14 flex items-center gap-3">
                    <MemberAvatar id={m.id} name={m.display_name} kind="human" size="md" />
                    <span className="text-[14.5px] font-medium text-fg">{m.display_name}</span>
                    {m.id === boot.current_member.id && <Badge>You</Badge>}
                    <span className="flex-1" />
                    <span className="text-[13px] text-fg-3 capitalize">{m.access_level}</span>
                  </div>
                ))}
              </div>
            </>
          )}

          {tab === "settings" && (
            <div className="space-y-8">
              <div>
                <SectionTitle title="Appearance" />
                <Segmented
                  value={theme}
                  onChange={onTheme}
                  options={[
                    { value: "system", label: <><SunMoon />System</> },
                    { value: "light", label: <><Sun />Light</> },
                    { value: "dark", label: <><Moon />Dark</> },
                  ]}
                />
              </div>
              <div>
                <SectionTitle title="Space" />
                <AccentPicker spaceSlug={boot.space.slug} value={boot.space.accent} disabled={boot.current_member.access_level === "member"} />
                <div className="rounded-[14px] border border-line bg-card divide-y divide-line text-[14px]">
                  {[
                    ["Name", boot.space.name],
                    ["Address", `/${boot.space.slug}`],
                    ["Your role", boot.current_member.access_level],
                  ].map(([k, v]) => (
                    <div key={k} className="px-4 h-12 flex items-center">
                      <span className="w-40 text-fg-3">{k}</span>
                      <span className="text-fg font-medium">{v}</span>
                    </div>
                  ))}
                </div>
              </div>
              <Button variant="danger" icon={<LogOut />} onClick={onLogout}>
                Sign out
              </Button>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
