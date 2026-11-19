import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AtSign, CheckCircle2, CircleDashed, Inbox, MessageSquare, Monitor, Plus, Users } from "lucide-react";
import {
  api,
  formatClock,
  formatDay,
  type BootstrapData,
  type InboxItem,
  type Task,
  type TaskStatus,
} from "../api";
import type { DetailTarget } from "./DetailPane";
import { MemberAvatar, RuntimeDot, TaskStatusGlyph } from "./Identity";
import { InboxSkeleton, RowListSkeleton } from "./Skeletons";
import { Button, EmptyState, Segmented, useAction } from "./ui";

export type InboxFilter = "needs_me" | "unread" | "all";
export type TaskFilter = "active" | "mine" | "done";

const REASON: Record<string, string> = {
  dm: "Direct message",
  mention: "Mentioned you",
  task: "Task",
  thread: "Thread",
  following: "Following",
  channel: "Channel",
  channel_all: "Channel",
};

function ViewHeader({ title, children }: { title: string; children?: React.ReactNode }) {
  return (
    <header className="h-11 px-4 border-b border-line flex items-center gap-3 shrink-0">
      <h1 className="text-[14px] font-semibold text-ink">{title}</h1>
      {children}
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

  function refresh() {
    void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  function dismiss(it: InboxItem, fn: () => Promise<unknown>) {
    setLeaving((s) => new Set(s).add(it.conversation_id));
    void run(fn).then((ok) => {
      if (!ok) {
        setLeaving((s) => {
          const n = new Set(s);
          n.delete(it.conversation_id);
          return n;
        });
      }
      refresh();
    });
  }

  function open(it: InboxItem) {
    if (it.kind === "task" && it.task_id) onOpenDetail({ kind: "task", taskID: it.task_id });
    else if (it.kind === "thread")
      onOpenDetail({ kind: "thread", threadConvID: it.conversation_id, rootMessageID: "", parentSlug: it.parent_slug ?? "" });
    else onOpenConversation(it.conversation_id);
  }

  const items = inboxQ.data ?? [];
  return (
    <section className="flex flex-col h-full min-h-0">
      <ViewHeader title="Inbox">
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
            icon={<Inbox className="w-4.5 h-4.5" />}
            title="You're all caught up"
            body={
              filter === "needs_me"
                ? "DMs, @mentions, followed threads and Tasks assigned to you show up here."
                : "Nothing unread right now."
            }
          />
        ) : (
          <div className="divide-y divide-line animate-fade-in">
            {items.map((it) => {
              const gone = leaving.has(it.conversation_id);
              return (
                <div
                  key={it.conversation_id}
                  className={`group px-4 py-2.5 flex items-center justify-between gap-4 transition-[opacity,transform,background-color] duration-200 ${
                    gone ? "opacity-0 translate-x-2 pointer-events-none" : ""
                  } ${it.needs_attention ? "bg-warm-soft hover:bg-[#FDF2EA]" : "hover:bg-paper-2"}`}
                >
                  <div className="min-w-0 flex-1 cursor-pointer" onClick={() => open(it)}>
                    <div className="flex items-center gap-2 text-[12px]">
                      {it.reason === "mention" ? (
                        <AtSign className="w-3.5 h-3.5 text-warm" />
                      ) : it.reason === "dm" ? (
                        <MessageSquare className="w-3.5 h-3.5 text-warm" />
                      ) : it.kind === "task" ? (
                        <CircleDashed className="w-3.5 h-3.5 text-mute" />
                      ) : null}
                      <span className={`font-semibold ${it.needs_attention ? "text-warm" : "text-ink-2"}`}>
                        {REASON[it.reason] ?? it.reason}
                      </span>
                      <span className="text-mute truncate">{it.title}</span>
                      {it.unread_count > 1 && (
                        <span className="font-mono-num text-[11px] text-faint">+{it.unread_count}</span>
                      )}
                      <span className="font-mono-num text-[11px] text-faint ml-auto shrink-0">{formatClock(it.latest_at)}</span>
                    </div>
                    <p className="mt-0.5 text-[13px] text-ink truncate">
                      {it.latest_author_name && <strong className="font-semibold">{it.latest_author_name}: </strong>}
                      <span className="text-ink-2">{it.latest_preview}</span>
                    </p>
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    {(it.kind === "thread" || it.kind === "task") && it.following && (
                      <Button
                        variant="ghost"
                        size="xs"
                        className="opacity-0 group-hover:opacity-100"
                        onClick={() => dismiss(it, () => api.setFollowing(spaceSlug, it.conversation_id, false))}
                      >
                        Unfollow
                      </Button>
                    )}
                    {(it.kind === "public" || it.kind === "private") && it.notify_level !== "mute" && (
                      <Button
                        variant="ghost"
                        size="xs"
                        className="opacity-0 group-hover:opacity-100"
                        onClick={() => dismiss(it, () => api.setNotifyLevel(spaceSlug, it.conversation_id, "mute"))}
                      >
                        Mute
                      </Button>
                    )}
                    <Button variant="secondary" size="xs" onClick={() => dismiss(it, () => api.markDone(spaceSlug, it.conversation_id))}>
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

const STATUS_ORDER: { status: TaskStatus; label: string }[] = [
  { status: "in_progress", label: "In Progress" },
  { status: "in_review", label: "In Review" },
  { status: "todo", label: "To Do" },
  { status: "done", label: "Done" },
  { status: "closed", label: "Closed" },
];

function TaskRow({ t, onOpen }: { t: Task; onOpen: () => void }) {
  return (
    <button onClick={onOpen} className="w-full h-9 px-4 hover:bg-paper-2 flex items-center gap-4 text-left transition-colors">
      <span className="flex items-center gap-2.5 min-w-0 flex-1">
        <TaskStatusGlyph status={t.status} />
        <span className="font-mono-num text-[12px] text-mute w-8 shrink-0">#{t.number}</span>
        <span className="text-[13px] font-medium text-ink truncate">{t.title}</span>
        {t.message_count > 0 && (
          <span className="inline-flex items-center gap-1 text-[11px] text-faint shrink-0">
            <MessageSquare className="w-3 h-3" />
            <span className="font-mono-num">{t.message_count}</span>
          </span>
        )}
      </span>
      <span className="w-32 flex items-center gap-1.5 text-[12px] text-ink-2 shrink-0 min-w-0">
        {t.assignee_member_id ? (
          <>
            <MemberAvatar id={t.assignee_member_id} name={t.assignee_display_name ?? "?"} kind={t.assignee_kind ?? "human"} size="sm" />
            <span className="truncate">{t.assignee_display_name}</span>
          </>
        ) : (
          <span className="text-faint">Unassigned</span>
        )}
      </span>
      <span className="w-28 text-right text-[12px] text-faint truncate shrink-0">{t.channel_slug ? `#${t.channel_slug}` : "DM"}</span>
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
    queryFn: () =>
      api.listTasks(spaceSlug, filter === "done" ? "done" : "active", filter === "mine" ? "mine" : "").then((r) => r.tasks),
  });
  const tasks = tasksQ.data ?? [];
  const groups = STATUS_ORDER.map((g) => ({ ...g, tasks: tasks.filter((t) => t.status === g.status) })).filter(
    (g) => g.tasks.length > 0,
  );

  return (
    <section className="flex flex-col h-full min-h-0">
      <ViewHeader title="Tasks">
        <Segmented
          value={filter}
          onChange={onFilter}
          options={[
            { value: "active", label: "Active" },
            { value: "mine", label: "Mine" },
            { value: "done", label: "Done" },
          ]}
        />
      </ViewHeader>
      <div className="flex-1 overflow-y-auto scroll-thin">
        {tasksQ.isLoading ? (
          <RowListSkeleton />
        ) : groups.length === 0 ? (
          <EmptyState
            icon={<CheckCircle2 className="w-4.5 h-4.5" />}
            title={filter === "done" ? "No finished tasks yet" : filter === "mine" ? "Nothing assigned to you" : "No active tasks"}
            body="Hover any message in a channel and choose Task to turn it into tracked work."
          />
        ) : (
          <div className="animate-fade-in">
            {groups.map((g) => (
              <div key={g.status}>
                <div className="sticky top-0 z-10 h-8 px-4 bg-paper/95 border-b border-line flex items-center gap-2 text-[11.5px] font-medium text-mute">
                  <TaskStatusGlyph status={g.status} />
                  <span>{g.label}</span>
                  <span className="font-mono-num text-faint">{g.tasks.length}</span>
                </div>
                <div className="divide-y divide-line">
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

export type AdminTab = "computers" | "members" | "settings";

export function AdminPanel({
  spaceSlug,
  boot,
  tab,
  onTab,
  onInspectAgent,
  onOpenSetup,
  onLogout,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  tab: AdminTab;
  onTab: (t: AdminTab) => void;
  onInspectAgent: (memberID: string) => void;
  onOpenSetup: (step: "computer" | "agent") => void;
  onLogout: () => void;
}) {
  const qc = useQueryClient();
  const [agName, setAgName] = useState("");
  const [agRole, setAgRole] = useState("");
  const [agDriver, setAgDriver] = useState<"builtin" | "acp">("builtin");
  const create = useAction();

  async function handleCreateAgent(e: FormEvent) {
    e.preventDefault();
    const comp = boot.computers.find((c) => c.connection_status === "online") ?? boot.computers[0];
    if (!comp || !agName.trim()) return;
    const ok = await create.run(() =>
      api.createAgent(spaceSlug, {
        display_name: agName.trim(),
        role_text: agRole.trim(),
        computer_id: comp.id,
        driver_kind: agDriver,
      }),
    );
    if (!ok) return;
    setAgName("");
    setAgRole("");
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  const humans = boot.members.filter((m) => m.kind === "human");
  const agents = boot.members.filter((m) => m.kind === "agent");

  return (
    <section className="flex flex-col h-full min-h-0">
      <ViewHeader title="Space">
        <Segmented
          value={tab}
          onChange={onTab}
          options={[
            { value: "computers", label: "Computers" },
            { value: "members", label: "Members" },
            { value: "settings", label: "Settings" },
          ]}
        />
      </ViewHeader>
      <div key={tab} className="flex-1 overflow-y-auto scroll-thin animate-fade-in">
        <div className="p-6 max-w-2xl space-y-6">
          {tab === "computers" && (
            <>
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h2 className="text-[14px] font-semibold">Computers</h2>
                  <p className="text-[12.5px] text-mute mt-0.5">Machines that host this space's agents.</p>
                </div>
                <Button onClick={() => onOpenSetup("computer")}>
                  <Plus className="w-3.5 h-3.5" /> Connect computer
                </Button>
              </div>
              {boot.computers.length === 0 ? (
                <div className="border border-dashed border-field rounded-[8px]">
                  <EmptyState
                    icon={<Monitor className="w-4.5 h-4.5" />}
                    title="No computers yet"
                    body="Open the Sumi desktop app on your Mac and confirm it, or run one command on any machine."
                    action={<Button onClick={() => onOpenSetup("computer")}>Connect a computer</Button>}
                  />
                </div>
              ) : (
                <div className="border border-line rounded-[8px] divide-y divide-line">
                  {boot.computers.map((c) => {
                    const hosted = agents.filter((a) => a.agent?.computer_id === c.id).length;
                    return (
                      <div key={c.id} className="h-12 px-3.5 flex items-center justify-between text-[13px]">
                        <span className="flex items-center gap-3 min-w-0">
                          <Monitor className="w-4 h-4 text-mute shrink-0" />
                          <span className="min-w-0">
                            <span className="block font-medium truncate">{c.name}</span>
                            <span className="block text-[11.5px] text-faint truncate">
                              {c.hostname} · {c.os === "macos" ? "macOS" : "Linux"} · {hosted} {hosted === 1 ? "agent" : "agents"}
                            </span>
                          </span>
                        </span>
                        <span
                          className={`inline-flex items-center gap-1.5 text-[12px] ${c.connection_status === "online" ? "text-work" : "text-faint"}`}
                        >
                          <span className={`w-1.5 h-1.5 rounded-full ${c.connection_status === "online" ? "bg-work" : "bg-[#BDB9AE]"}`} />
                          {c.connection_status === "online" ? "Online" : c.last_seen_at ? `Last seen ${formatDay(c.last_seen_at)} ${formatClock(c.last_seen_at)}` : "Offline"}
                        </span>
                      </div>
                    );
                  })}
                </div>
              )}
            </>
          )}

          {tab === "members" && (
            <>
              <div>
                <h2 className="text-[14px] font-semibold">Agents</h2>
                <p className="text-[12.5px] text-mute mt-0.5">Persistent teammates. Select one to manage its runtime and permissions.</p>
              </div>
              {boot.computers.length === 0 ? (
                <div className="p-3.5 rounded-[8px] border border-line bg-paper-2 text-[12.5px] text-mute flex items-center justify-between gap-3">
                  Agents need a computer to run on.
                  <Button variant="secondary" onClick={() => onOpenSetup("computer")}>
                    Connect computer
                  </Button>
                </div>
              ) : (
                <form onSubmit={handleCreateAgent} className="p-3.5 border border-line rounded-[8px] space-y-2.5">
                  <div className="grid grid-cols-[1fr_auto] gap-2">
                    <input
                      value={agName}
                      onChange={(e) => setAgName(e.target.value.replace(/[^A-Za-z0-9_]/g, "").slice(0, 32))}
                      placeholder="Name, e.g. Lin"
                      className="h-8 px-2.5 rounded-[6px] border border-field focus:border-ink text-[13px] outline-none transition-colors"
                      required
                    />
                    <Segmented
                      value={agDriver}
                      onChange={setAgDriver}
                      options={[
                        { value: "builtin", label: "Builtin" },
                        { value: "acp", label: "ACP" },
                      ]}
                    />
                  </div>
                  <input
                    value={agRole}
                    onChange={(e) => setAgRole(e.target.value)}
                    placeholder="Role, e.g. Backend engineer who reviews Go changes"
                    className="w-full h-8 px-2.5 rounded-[6px] border border-field focus:border-ink text-[13px] outline-none transition-colors"
                  />
                  <Button type="submit" loading={create.busy} disabled={!agName}>
                    <Plus className="w-3.5 h-3.5" /> Create agent
                  </Button>
                </form>
              )}
              {agents.length > 0 && (
                <div className="border border-line rounded-[8px] divide-y divide-line">
                  {agents.map((m) => (
                    <button
                      key={m.id}
                      onClick={() => onInspectAgent(m.id)}
                      className="w-full h-11 px-3.5 flex items-center justify-between text-[13px] hover:bg-paper-2 transition-colors text-left"
                    >
                      <span className="flex items-center gap-2.5 min-w-0">
                        <MemberAvatar id={m.id} name={m.display_name} kind="agent" />
                        <span className="min-w-0">
                          <span className="block font-medium truncate">{m.display_name}</span>
                          <span className="block text-[11.5px] text-faint truncate">{m.agent?.role_text || "No role"}</span>
                        </span>
                      </span>
                      <span className="inline-flex items-center gap-1.5 text-[12px] text-mute capitalize shrink-0">
                        <RuntimeDot state={m.agent?.runtime_state} />
                        {m.agent?.runtime_state ?? "offline"}
                      </span>
                    </button>
                  ))}
                </div>
              )}

              <div>
                <h2 className="text-[14px] font-semibold flex items-center gap-2">
                  <Users className="w-4 h-4 text-mute" /> People
                </h2>
              </div>
              <div className="border border-line rounded-[8px] divide-y divide-line">
                {humans.map((m) => (
                  <div key={m.id} className="h-11 px-3.5 flex items-center justify-between text-[13px]">
                    <span className="flex items-center gap-2.5">
                      <MemberAvatar id={m.id} name={m.display_name} kind="human" />
                      <span className="font-medium">{m.display_name}</span>
                      {m.id === boot.current_member.id && <span className="text-[11.5px] text-faint">you</span>}
                    </span>
                    <span className="text-[12px] text-mute capitalize">{m.access_level}</span>
                  </div>
                ))}
              </div>
            </>
          )}

          {tab === "settings" && (
            <div className="space-y-4 text-[13px]">
              <div className="grid grid-cols-[120px_1fr] gap-y-2.5 border border-line rounded-[8px] p-4">
                <span className="text-mute">Space</span>
                <span className="font-medium">{boot.space.name}</span>
                <span className="text-mute">Address</span>
                <span className="font-mono-num">/{boot.space.slug}</span>
                <span className="text-mute">Account</span>
                <span>
                  {boot.current_member.display_name} <span className="text-faint capitalize">· {boot.current_member.access_level}</span>
                </span>
              </div>
              <Button variant="danger" onClick={onLogout}>
                Sign out
              </Button>
            </div>
          )}
        </div>
      </div>
    </section>
  );
}
