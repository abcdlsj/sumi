import { useEffect, useMemo, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Inbox, Monitor, Plus, Settings, Users } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  api,
  formatClock,
  type BootstrapData,
  type Conversation,
  type NotifyLevel,
} from "../api";
import { DetailPane, type DetailTarget } from "./DetailPane";
import { MemberAvatar, RuntimeDot, TaskStatusGlyph } from "./Identity";

type MainView =
  | { kind: "conversation"; convID: string }
  | { kind: "inbox" }
  | { kind: "tasks" }
  | { kind: "admin"; tab: "computers" | "members" | "settings" };

export function Workbench({
  spaceSlug,
  onLogout,
}: {
  spaceSlug: string;
  onLogout: () => void;
}) {
  const qc = useQueryClient();
  const bootQ = useQuery({
    queryKey: ["bootstrap", spaceSlug],
    queryFn: () => api.bootstrap(spaceSlug),
  });

  const [mainView, setMainView] = useState<MainView>({ kind: "inbox" });
  const [detail, setDetail] = useState<DetailTarget | null>(null);
  const [inboxFilter, setInboxFilter] = useState<"needs_me" | "unread" | "all">("needs_me");
  const [taskFilter, setTaskFilter] = useState<"active" | "mine" | "done">("active");
  const [composer, setComposer] = useState("");
  const [replyingTo, setReplyingTo] = useState<string | null>(null);
  const [creatingTaskOnMsg, setCreatingTaskOnMsg] = useState<string | null>(null);
  const [newTaskTitle, setNewTaskTitle] = useState("");

  // Default to #general once bootstrap arrives.
  useEffect(() => {
    if (!bootQ.data) return;
    if (mainView.kind === "inbox" && bootQ.data.inbox.length === 0 && bootQ.data.conversations.length > 0) {
      setMainView({ kind: "conversation", convID: bootQ.data.conversations[0].id });
    }
  }, [bootQ.data?.space.id]);

  // Single SSE connection per Space for live updates.
  useEffect(() => {
    const es = new EventSource(`/api/v1/spaces/${spaceSlug}/events`, { withCredentials: true });
    const invalidate = () => {
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug] });
    };
    for (const evName of [
      "message.created",
      "task.created",
      "task.updated",
      "agent.created",
      "agent.state_changed",
      "computer.changed",
      "conversation.created",
      "member.permissions_changed",
    ]) {
      es.addEventListener(evName, invalidate);
    }
    return () => es.close();
  }, [spaceSlug, qc]);

  const boot = bootQ.data;
  const activeConvID = mainView.kind === "conversation" ? mainView.convID : "";
  const activeConv = useMemo(
    () => boot?.conversations.find((c) => c.id === activeConvID),
    [boot, activeConvID],
  );

  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, activeConvID],
    queryFn: () => api.listMessages(spaceSlug, activeConvID, true),
    enabled: Boolean(activeConvID),
  });

  const inboxQ = useQuery({
    queryKey: ["inbox", spaceSlug, inboxFilter],
    queryFn: () => api.listInbox(spaceSlug, inboxFilter).then((r) => r.items),
    enabled: mainView.kind === "inbox",
  });

  const tasksQ = useQuery({
    queryKey: ["tasks", spaceSlug, taskFilter],
    queryFn: () =>
      api
        .listTasks(spaceSlug, taskFilter === "done" ? "done" : "active", taskFilter === "mine" ? "mine" : "")
        .then((r) => r.tasks),
    enabled: mainView.kind === "tasks",
  });

  if (!boot) {
    return (
      <div className="h-full bg-[#F4F2EC] flex items-center justify-center text-[13px] text-[#6E6B64]">
        Loading {spaceSlug}…
      </div>
    );
  }

  const channels = boot.conversations.filter((c) => c.kind === "public" || c.kind === "private");
  const dms = boot.conversations.filter((c) => c.kind === "dm");
  const agents = boot.members.filter((m) => m.kind === "agent");
  const onlineComputers = boot.computers.filter((c) => c.connection_status === "online").length;

  async function handleSendMessage(e: FormEvent) {
    e.preventDefault();
    if (!activeConv || !composer.trim()) return;
    await api.sendMessage(spaceSlug, activeConv.id, composer.trim(), replyingTo ?? undefined);
    setComposer("");
    setReplyingTo(null);
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, activeConv.id] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function handleCreateInlineTask(e: FormEvent, sourceMsgID: string) {
    e.preventDefault();
    if (!activeConv || !newTaskTitle.trim()) return;
    const res = await api.createTask(spaceSlug, {
      channel_id: activeConv.id,
      source_message_id: sourceMsgID,
      title: newTaskTitle.trim(),
    });
    setNewTaskTitle("");
    setCreatingTaskOnMsg(null);
    setDetail({ kind: "task", taskID: res.task.id });
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, activeConv.id] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function cycleNotify(conv: Conversation) {
    const order: NotifyLevel[] = ["mentions", "all", "mute"];
    const next = order[(order.indexOf(conv.notify_level ?? "mentions") + 1) % order.length];
    await api.setNotifyLevel(spaceSlug, conv.id, next);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function openOrCreateDM(peerMemberID: string) {
    const res = await api.createDM(spaceSlug, peerMemberID);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    setMainView({ kind: "conversation", convID: res.conversation.id });
  }

  const workingAgent = agents.find((a) => a.agent?.runtime_state === "working");
  const errorAgent = agents.find((a) => a.agent?.runtime_state === "error");

  return (
    <div
      className="h-full grid bg-white"
      style={{
        gridTemplateColumns: detail ? "44px 228px minmax(0, 1fr) 388px" : "44px 228px minmax(0, 1fr)",
      }}
    >
      {/* 0. ANCHOR RAIL (44px) */}
      <nav className="bg-[#ECEAE3] border-r border-[#DCD9D0] flex flex-col items-center justify-between py-2 select-none">
        <div className="flex flex-col items-center gap-2">
          <button
            onClick={() => channels[0] && setMainView({ kind: "conversation", convID: channels[0].id })}
            title={boot.space.name}
            className="w-7 h-7 rounded-[6px] bg-[#161614] text-white text-[12px] font-semibold flex items-center justify-center"
          >
            {(boot.space.name[0] ?? "S").toUpperCase()}
          </button>
        </div>
        <div className="flex flex-col items-center gap-1.5">
          <button
            onClick={() => setMainView({ kind: "admin", tab: "computers" })}
            title={`Computers (${onlineComputers} online)`}
            className="w-7 h-7 rounded-[5px] hover:bg-[#E0DDD4] text-[#5C5A54] flex items-center justify-center relative"
          >
            <Monitor className="w-4 h-4" />
            <span
              className={`w-1.5 h-1.5 rounded-full absolute top-1 right-1 ${
                onlineComputers > 0 ? "bg-[#0F8B78]" : "bg-[#BDB9AE]"
              }`}
            />
          </button>
          <button
            onClick={() => setMainView({ kind: "admin", tab: "members" })}
            title="Members & Permissions"
            className="w-7 h-7 rounded-[5px] hover:bg-[#E0DDD4] text-[#5C5A54] flex items-center justify-center"
          >
            <Users className="w-4 h-4" />
          </button>
          <button
            onClick={() => setMainView({ kind: "admin", tab: "settings" })}
            title="Settings"
            className="w-7 h-7 rounded-[5px] hover:bg-[#E0DDD4] text-[#5C5A54] flex items-center justify-center"
          >
            <Settings className="w-4 h-4" />
          </button>
        </div>
      </nav>

      {/* 1. SIDEBAR (228px — tight 28px rows) */}
      <aside className="bg-[#F4F2EC] border-r border-[#DCD9D0] flex flex-col justify-between min-h-0 select-none">
        <div className="flex flex-col min-h-0">
          <div className="h-11 px-3 border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
            <span className="text-[13.5px] font-semibold text-[#161614] truncate">{boot.space.name}</span>
          </div>

          <div className="p-2 space-y-4 overflow-y-auto scroll-thin">
            <div className="space-y-0.5">
              <button
                onClick={() => setMainView({ kind: "inbox" })}
                className={`w-full h-7 px-2 rounded-[5px] flex items-center justify-between text-[13px] ${
                  mainView.kind === "inbox" ? "bg-[#E4E1D8] text-[#161614] font-semibold" : "text-[#45433E] hover:bg-[#E9E6DE]"
                }`}
              >
                <span className="flex items-center gap-2">
                  <Inbox className="w-3.5 h-3.5 text-[#6E6B64]" />
                  <span>Inbox</span>
                </span>
                {boot.inbox.length > 0 && (
                  <span className="px-1.5 rounded text-[11px] font-semibold font-mono-num bg-[#F7DFD1] text-[#A84010]">
                    {boot.inbox.length}
                  </span>
                )}
              </button>
              <button
                onClick={() => setMainView({ kind: "tasks" })}
                className={`w-full h-7 px-2 rounded-[5px] flex items-center justify-between text-[13px] ${
                  mainView.kind === "tasks" ? "bg-[#E4E1D8] text-[#161614] font-semibold" : "text-[#45433E] hover:bg-[#E9E6DE]"
                }`}
              >
                <span className="flex items-center gap-2">
                  <CheckCircle2 className="w-3.5 h-3.5 text-[#6E6B64]" />
                  <span>Tasks</span>
                </span>
                <span className="text-[11px] font-mono-num text-[#6E6B64]">{boot.tasks.length}</span>
              </button>
            </div>

            {/* Channels */}
            <div>
              <div className="px-2 mb-1 flex items-center justify-between text-[11.5px] font-medium text-[#6E6B64]">
                <span>Channels</span>
                <button
                  onClick={() => {
                    const slug = window.prompt("Channel slug (lowercase, hyphens):");
                    if (slug) {
                      void api.createChannel(spaceSlug, slug.trim(), "public", "").then((r) => {
                        void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                        setMainView({ kind: "conversation", convID: r.conversation.id });
                      });
                    }
                  }}
                  title="New channel"
                  className="hover:text-[#161614]"
                >
                  <Plus className="w-3.5 h-3.5" />
                </button>
              </div>
              <div className="space-y-0.5">
                {channels.map((c) => {
                  const isActive = mainView.kind === "conversation" && mainView.convID === c.id;
                  const muted = c.notify_level === "mute";
                  return (
                    <button
                      key={c.id}
                      onClick={() => setMainView({ kind: "conversation", convID: c.id })}
                      className={`w-full h-7 px-2 rounded-[5px] flex items-center justify-between text-[13px] ${
                        isActive
                          ? "bg-[#E4E1D8] text-[#161614] font-semibold"
                          : muted
                            ? "text-[#8A867D] hover:bg-[#E9E6DE]"
                            : c.unread_count > 0
                              ? "text-[#161614] font-semibold hover:bg-[#E9E6DE]"
                              : "text-[#45433E] hover:bg-[#E9E6DE]"
                      }`}
                    >
                      <span className="flex items-center gap-1.5 truncate">
                        <span className="text-[#6E6B64] font-normal">#</span>
                        <span className="truncate">{c.slug}</span>
                      </span>
                      {c.mention_count > 0 && !muted ? (
                        <span className="px-1.5 rounded text-[11px] font-semibold font-mono-num bg-[#F7DFD1] text-[#A84010]">
                          {c.mention_count}
                        </span>
                      ) : muted ? (
                        <span className="text-[10.5px] text-[#9A968D]">muted</span>
                      ) : c.unread_count > 0 ? (
                        <span className="w-1.5 h-1.5 rounded-full bg-[#161614]" />
                      ) : null}
                    </button>
                  );
                })}
              </div>
            </div>

            {/* Direct Messages & Agents */}
            <div>
              <div className="px-2 mb-1 text-[11.5px] font-medium text-[#6E6B64]">Direct messages</div>
              <div className="space-y-0.5">
                {boot.members
                  .filter((m) => m.id !== boot.current_member.id)
                  .map((m) => {
                    const dm = dms.find((d) => d.peer_member_id === m.id);
                    const isActive = Boolean(dm && mainView.kind === "conversation" && mainView.convID === dm.id);
                    return (
                      <button
                        key={m.id}
                        onClick={() => void openOrCreateDM(m.id)}
                        className={`w-full h-7 px-2 rounded-[5px] flex items-center justify-between text-[13px] ${
                          isActive ? "bg-[#E4E1D8] text-[#161614] font-semibold" : "text-[#45433E] hover:bg-[#E9E6DE]"
                        }`}
                      >
                        <span className="flex items-center gap-2 min-w-0">
                          <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" />
                          <span className="truncate">{m.display_name}</span>
                        </span>
                        {dm && dm.unread_count > 0 ? (
                          <span className="px-1.5 rounded text-[11px] font-semibold font-mono-num bg-[#F7DFD1] text-[#A84010]">
                            {dm.unread_count}
                          </span>
                        ) : m.kind === "agent" ? (
                          <RuntimeDot state={m.agent?.runtime_state} />
                        ) : null}
                      </button>
                    );
                  })}
              </div>
            </div>
          </div>
        </div>

        <div className="h-10 px-3 border-t border-[#E4E1D9] flex items-center justify-between text-[12.5px]">
          <div className="flex items-center gap-2 min-w-0">
            <MemberAvatar id={boot.current_member.id} name={boot.current_member.display_name} kind="human" size="sm" />
            <span className="font-medium text-[#161614] truncate">{boot.current_member.display_name}</span>
          </div>
          <span className="text-[11px] text-[#8A867D]">{boot.current_member.access_level}</span>
        </div>
      </aside>

      {/* 2. MAIN PANE */}
      <main className="bg-white flex flex-col min-w-0 min-h-0">
        {mainView.kind === "conversation" && activeConv && (
          <section className="flex flex-col h-full min-h-0">
            <header className="h-11 px-4 border-b border-[#E4E1D9] flex items-center justify-between gap-4 shrink-0">
              <div className="flex items-center gap-2.5 min-w-0">
                <h1 className="text-[14px] font-semibold text-[#161614] shrink-0">
                  {activeConv.kind === "dm" ? `@${activeConv.peer_display_name}` : `#${activeConv.slug}`}
                </h1>
                {activeConv.topic && (
                  <>
                    <span className="text-[#D5D1C7]">|</span>
                    <span className="text-[12.5px] text-[#6E6B64] truncate">{activeConv.topic}</span>
                  </>
                )}
              </div>
              <div className="flex items-center gap-2 shrink-0">
                {activeConv.kind !== "dm" && (
                  <button
                    onClick={() => void cycleNotify(activeConv)}
                    className="h-7 px-2 rounded border border-[#E4E1D9] hover:bg-[#F4F2EC] text-[12px] text-[#45433E] flex items-center gap-1.5"
                  >
                    <span
                      className={`w-1.5 h-1.5 rounded-full ${
                        activeConv.notify_level === "mute"
                          ? "bg-[#9A968D]"
                          : activeConv.notify_level === "all"
                            ? "bg-[#161614]"
                            : "bg-[#A84010]"
                      }`}
                    />
                    <span>
                      {activeConv.notify_level === "all"
                        ? "All messages"
                        : activeConv.notify_level === "mute"
                          ? "Muted"
                          : "Mentions only"}
                    </span>
                  </button>
                )}
              </div>
            </header>

            <div className="flex-1 overflow-y-auto scroll-thin py-3">
              {(msgsQ.data?.messages ?? []).map((m, idx, arr) => {
                const prevRead = msgsQ.data?.read_seq ?? 0;
                const showNewDivider =
                  prevRead > 0 &&
                  m.seq > prevRead &&
                  m.author_member_id !== boot.current_member.id &&
                  (idx === 0 || arr[idx - 1].seq <= prevRead);
                return (
                  <div key={m.id}>
                    {showNewDivider && (
                      <div className="px-5 py-1.5 flex items-center gap-2.5">
                        <div className="flex-1 h-px bg-[#E3AE91]" />
                        <span className="text-[10.5px] font-semibold uppercase tracking-wider text-[#A84010]">New</span>
                        <div className="flex-1 h-px bg-[#E3AE91]" />
                      </div>
                    )}
                    {m.kind === "action" ? (
                      <div className="px-5 py-1.5 flex items-center gap-3 text-[12px] text-[#6E6B64]">
                        <span className="w-7 flex justify-end">
                          <span className="w-4 h-4 rounded bg-[#F1EFEA] border border-[#E4E1D9] flex items-center justify-center text-[10px]">
                            +
                          </span>
                        </span>
                        <span>
                          <strong className="font-medium text-[#161614]">{m.author_display_name}</strong>{" "}
                          {m.action_kind} · <span className="font-mono-num text-[11px]">{formatClock(m.created_at)}</span>
                        </span>
                      </div>
                    ) : (
                      <article className="px-5 py-2 hover:bg-[#FAF9F6] flex items-start gap-3 group">
                        <button
                          onClick={() =>
                            m.author_kind === "agent" && setDetail({ kind: "agent", memberID: m.author_member_id })
                          }
                          className="mt-0.5"
                        >
                          <MemberAvatar
                            id={m.author_member_id}
                            name={m.author_display_name ?? "?"}
                            kind={m.author_kind ?? "human"}
                          />
                        </button>
                        <div className="min-w-0 flex-1 max-w-[70ch]">
                          <div className="flex items-center justify-between gap-2">
                            <div className="flex items-baseline gap-2">
                              <span className="text-[13.5px] font-semibold text-[#161614]">{m.author_display_name}</span>
                              <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(m.created_at)}</span>
                            </div>
                            <div className="opacity-0 group-hover:opacity-100 flex items-center gap-1 text-[11.5px] text-[#6E6B64]">
                              <button
                                onClick={() => setReplyingTo(m.id)}
                                className="px-1.5 py-0.5 rounded hover:bg-[#E9E6DE] hover:text-[#161614]"
                              >
                                Reply
                              </button>
                              <button
                                onClick={() => setCreatingTaskOnMsg(m.id)}
                                className="px-1.5 py-0.5 rounded hover:bg-[#E9E6DE] hover:text-[#161614]"
                              >
                                + Task
                              </button>
                            </div>
                          </div>
                          <div className="mt-0.5 text-[13.5px] leading-[1.52] text-[#161614] prose prose-sm max-w-none">
                            <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.body_markdown}</ReactMarkdown>
                          </div>

                          {/* Compact 32px Task Table (1:N per root message) */}
                          {m.attached_tasks && m.attached_tasks.length > 0 && (
                            <div className="mt-2 border border-[#E4E1D9] rounded-[6px] bg-[#FAF9F6] divide-y divide-[#E4E1D9] overflow-hidden">
                              {m.attached_tasks.map((tk) => (
                                <button
                                  key={tk.id}
                                  onClick={() => setDetail({ kind: "task", taskID: tk.id })}
                                  className="w-full h-8 px-2.5 hover:bg-[#F2F0EA] flex items-center justify-between gap-3 text-left"
                                >
                                  <span className="flex items-center gap-2 min-w-0">
                                    <TaskStatusGlyph status={tk.status} />
                                    <span className="font-mono-num text-[11.5px] text-[#6E6B64] w-7 shrink-0">
                                      #{tk.number}
                                    </span>
                                    <span className="text-[12.5px] font-medium text-[#161614] truncate">{tk.title}</span>
                                  </span>
                                  <span className="flex items-center gap-3 shrink-0 text-[11.5px] text-[#6E6B64]">
                                    <span>{tk.assignee_display_name || "Unassigned"}</span>
                                    {tk.message_count > 0 && (
                                      <span className="font-mono-num text-[#8A867D]">{tk.message_count} msgs</span>
                                    )}
                                  </span>
                                </button>
                              ))}
                            </div>
                          )}

                          {/* Inline Quick Task Creation */}
                          {creatingTaskOnMsg === m.id && (
                            <form
                              onSubmit={(e) => void handleCreateInlineTask(e, m.id)}
                              className="mt-2 flex items-center gap-1.5"
                            >
                              <input
                                autoFocus
                                value={newTaskTitle}
                                onChange={(e) => setNewTaskTitle(e.target.value)}
                                placeholder="New task title…"
                                className="flex-1 h-7 px-2 rounded border border-[#161614] text-[12.5px] outline-none"
                              />
                              <button
                                type="submit"
                                className="h-7 px-2.5 rounded bg-[#161614] text-white text-[12px] font-medium"
                              >
                                Create
                              </button>
                              <button
                                type="button"
                                onClick={() => setCreatingTaskOnMsg(null)}
                                className="h-7 px-2 rounded text-[12px] text-[#6E6B64] hover:bg-[#F4F2EC]"
                              >
                                Cancel
                              </button>
                            </form>
                          )}

                          {/* Compact Single-line Thread Indicator */}
                          {m.reply_count && m.reply_count > 0 && m.thread_conversation_id ? (
                            <button
                              onClick={() =>
                                setDetail({
                                  kind: "thread",
                                  threadConvID: m.thread_conversation_id!,
                                  rootMessageID: m.id,
                                  parentSlug: activeConv.slug ?? "dm",
                                })
                              }
                              className="mt-1.5 inline-flex items-center gap-2 text-[12px] text-[#45433E] hover:text-[#161614]"
                            >
                              <span className="font-medium underline decoration-[#D5D1C7] underline-offset-2">
                                {m.reply_count} {m.reply_count === 1 ? "reply" : "replies"}
                              </span>
                              <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(m.last_reply_at)}</span>
                            </button>
                          ) : null}
                        </div>
                      </article>
                    )}
                  </div>
                );
              })}
            </div>

            {/* Grounded Docked Footer: 28px Honest Agent Status + Structural Composer */}
            <div className="border-t border-[#E4E1D9] bg-[#FAF9F6] shrink-0">
              <div className="h-7 px-4 border-b border-[#ECEAE3] flex items-center justify-between text-[12px]">
                {workingAgent ? (
                  <button
                    onClick={() => setDetail({ kind: "agent", memberID: workingAgent.id })}
                    className="inline-flex items-center gap-2 text-[#45433E] hover:text-[#161614]"
                  >
                    <RuntimeDot state="working" />
                    <span>
                      <strong className="font-medium text-[#161614]">{workingAgent.display_name}</strong> working
                    </span>
                  </button>
                ) : errorAgent ? (
                  <button
                    onClick={() => setDetail({ kind: "agent", memberID: errorAgent.id })}
                    className="inline-flex items-center gap-2 text-[#A84010]"
                  >
                    <RuntimeDot state="error" />
                    <span>
                      <strong>{errorAgent.display_name}</strong> error · {errorAgent.agent?.last_error_code || "needs attention"}
                    </span>
                  </button>
                ) : (
                  <span className="text-[#8A867D]">
                    {replyingTo ? "Replying in thread" : "All agents idle"}
                  </span>
                )}
                {replyingTo && (
                  <button onClick={() => setReplyingTo(null)} className="text-[11.5px] text-[#6E6B64] hover:text-[#161614]">
                    Cancel thread reply
                  </button>
                )}
              </div>

              <form onSubmit={handleSendMessage} className="bg-white">
                <textarea
                  rows={2}
                  value={composer}
                  onChange={(e) => setComposer(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                      e.preventDefault();
                      void handleSendMessage(e);
                    }
                  }}
                  placeholder={
                    replyingTo
                      ? "Reply in thread…"
                      : `Message ${activeConv.kind === "dm" ? "@" + activeConv.peer_display_name : "#" + activeConv.slug}`
                  }
                  className="w-full px-4 pt-2.5 pb-1 text-[13.5px] bg-transparent outline-none resize-none placeholder:text-[#8A867D]"
                />
                <div className="px-3 pb-2 flex items-center justify-end gap-2">
                  <span className="font-mono-num text-[11px] text-[#8A867D]">↵ Send</span>
                  <button
                    disabled={!composer.trim()}
                    type="submit"
                    className="h-6 px-2.5 rounded-[5px] bg-[#161614] hover:bg-[#2F2E2B] text-white text-[12px] font-medium disabled:opacity-40"
                  >
                    Send
                  </button>
                </div>
              </form>
            </div>
          </section>
        )}

        {mainView.kind === "inbox" && (
          <section className="flex flex-col h-full min-h-0">
            <header className="h-11 px-4 border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
              <div className="flex items-center gap-3">
                <h1 className="text-[14px] font-semibold text-[#161614]">Inbox</h1>
                <div className="flex items-center gap-1 text-[12px]">
                  {(["needs_me", "unread", "all"] as const).map((f) => (
                    <button
                      key={f}
                      onClick={() => setInboxFilter(f)}
                      className={`px-2 py-0.5 rounded ${
                        inboxFilter === f ? "bg-[#E9E6DE] text-[#161614] font-medium" : "text-[#6E6B64] hover:bg-[#F4F2EC]"
                      }`}
                    >
                      {f === "needs_me" ? "Needs me" : f === "unread" ? "Unread" : "All"}
                    </button>
                  ))}
                </div>
              </div>
            </header>
            <div className="flex-1 overflow-y-auto scroll-thin divide-y divide-[#E4E1D9]">
              {(inboxQ.data ?? []).length === 0 ? (
                <div className="p-10 text-center text-[12.5px] text-[#8A867D]">Inbox zero.</div>
              ) : (
                (inboxQ.data ?? []).map((it) => (
                  <div
                    key={it.conversation_id}
                    className={`px-4 py-2.5 flex items-center justify-between gap-4 ${
                      it.needs_attention ? "bg-[#FFF9F5] hover:bg-[#FDF2EA]" : "hover:bg-[#FAF9F6]"
                    }`}
                  >
                    <div
                      className="min-w-0 flex-1 cursor-pointer"
                      onClick={() => {
                        if (it.kind === "task" && it.task_id) {
                          setDetail({ kind: "task", taskID: it.task_id });
                        } else if (it.kind === "thread") {
                          setDetail({
                            kind: "thread",
                            threadConvID: it.conversation_id,
                            rootMessageID: "",
                            parentSlug: it.parent_slug ?? "",
                          });
                        } else {
                          setMainView({ kind: "conversation", convID: it.conversation_id });
                        }
                      }}
                    >
                      <div className="flex items-center gap-2 text-[12px]">
                        <span className={`font-semibold ${it.needs_attention ? "text-[#A84010]" : "text-[#45433E]"}`}>
                          {it.reason}
                        </span>
                        <span className="text-[#6E6B64]">{it.title}</span>
                        <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(it.latest_at)}</span>
                      </div>
                      <p className="mt-0.5 text-[13px] text-[#161614] truncate">
                        <strong>{it.latest_author_name}:</strong> {it.latest_preview}
                      </p>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <button
                        onClick={() =>
                          void api.markDone(spaceSlug, it.conversation_id).then(() => {
                            void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
                            void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                          })
                        }
                        className="h-6 px-2 rounded border border-[#D5D1C7] bg-white hover:border-[#161614] text-[11.5px] font-medium"
                      >
                        Done
                      </button>
                    </div>
                  </div>
                ))
              )}
            </div>
          </section>
        )}

        {mainView.kind === "tasks" && (
          <section className="flex flex-col h-full min-h-0">
            <header className="h-11 px-4 border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
              <div className="flex items-center gap-3">
                <h1 className="text-[14px] font-semibold text-[#161614]">Tasks</h1>
                <div className="flex items-center gap-1 text-[12px]">
                  {(["active", "mine", "done"] as const).map((f) => (
                    <button
                      key={f}
                      onClick={() => setTaskFilter(f)}
                      className={`px-2 py-0.5 rounded capitalize ${
                        taskFilter === f ? "bg-[#E9E6DE] text-[#161614] font-medium" : "text-[#6E6B64] hover:bg-[#F4F2EC]"
                      }`}
                    >
                      {f}
                    </button>
                  ))}
                </div>
              </div>
            </header>
            <div className="flex-1 overflow-y-auto scroll-thin divide-y divide-[#E4E1D9]">
              {(tasksQ.data ?? []).map((t) => (
                <button
                  key={t.id}
                  onClick={() => setDetail({ kind: "task", taskID: t.id })}
                  className="w-full h-9 px-4 hover:bg-[#FAF9F6] flex items-center justify-between gap-4 text-left"
                >
                  <span className="flex items-center gap-2.5 min-w-0">
                    <TaskStatusGlyph status={t.status} />
                    <span className="font-mono-num text-[12px] text-[#6E6B64] w-7 shrink-0">#{t.number}</span>
                    <span className="text-[13px] font-medium text-[#161614] truncate">{t.title}</span>
                  </span>
                  <span className="flex items-center gap-4 text-[12px] text-[#6E6B64] shrink-0">
                    <span className="w-20 text-right truncate">{t.assignee_display_name || "—"}</span>
                    <span className="w-24 text-right text-[#8A867D] truncate">
                      {t.channel_slug ? `#${t.channel_slug}` : "DM"}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          </section>
        )}

        {mainView.kind === "admin" && (
          <AdminPanel
            spaceSlug={spaceSlug}
            boot={boot}
            tab={mainView.tab}
            onInspectAgent={(id) => setDetail({ kind: "agent", memberID: id })}
            onLogout={onLogout}
          />
        )}
      </main>

      {/* 3. RIGHT DETAIL PANE */}
      {detail && (
        <DetailPane
          spaceSlug={spaceSlug}
          boot={boot}
          target={detail}
          onClose={() => setDetail(null)}
          onSelectTarget={setDetail}
        />
      )}
    </div>
  );
}

function AdminPanel({
  spaceSlug,
  boot,
  tab,
  onInspectAgent,
  onLogout,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  tab: "computers" | "members" | "settings";
  onInspectAgent: (memberID: string) => void;
  onLogout: () => void;
}) {
  const qc = useQueryClient();
  const [compName, setCompName] = useState("mac-local");
  const [pairedToken, setPairedToken] = useState("");
  const [agName, setAgName] = useState("");
  const [agRole, setAgRole] = useState("");
  const [agDriver, setAgDriver] = useState<"builtin" | "acp">("builtin");

  async function handlePair(e: FormEvent) {
    e.preventDefault();
    const res = await api.pairComputer(spaceSlug, compName, compName, "macos");
    setPairedToken(res.token);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function handleCreateAgent(e: FormEvent) {
    e.preventDefault();
    const comp = boot.computers[0];
    if (!comp || !agName.trim()) return;
    await api.createAgent(spaceSlug, {
      display_name: agName.trim(),
      role_text: agRole.trim(),
      computer_id: comp.id,
      driver_kind: agDriver,
    });
    setAgName("");
    setAgRole("");
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  return (
    <section className="flex flex-col h-full min-h-0">
      <header className="h-11 px-4 border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
        <h1 className="text-[14px] font-semibold text-[#161614] capitalize">{tab}</h1>
      </header>
      <div className="flex-1 overflow-y-auto scroll-thin p-5 max-w-2xl space-y-6">
        {tab === "computers" && (
          <>
            <form onSubmit={handlePair} className="flex items-center gap-2">
              <input
                value={compName}
                onChange={(e) => setCompName(e.target.value)}
                placeholder="Computer name"
                className="h-8 px-2.5 rounded border border-[#D5D1C7] text-[13px] outline-none"
              />
              <button type="submit" className="h-8 px-3 rounded bg-[#161614] text-white text-[12.5px] font-medium">
                Pair Computer
              </button>
            </form>
            {pairedToken && (
              <div className="p-3 rounded border border-[#E4E1D9] bg-[#FAF9F6] text-[12px] space-y-1">
                <div className="font-medium text-[#161614]">Run on your machine:</div>
                <code className="block font-mono-num break-all text-[#45433E]">
                  sumi computer --server {window.location.origin} --token {pairedToken}
                </code>
              </div>
            )}
            <div className="border border-[#E4E1D9] rounded-[6px] divide-y divide-[#E4E1D9]">
              {boot.computers.map((c) => (
                <div key={c.id} className="h-10 px-3 flex items-center justify-between text-[13px]">
                  <span className="font-medium">{c.name}</span>
                  <span className={c.connection_status === "online" ? "text-[#0F8B78]" : "text-[#8A867D]"}>
                    {c.connection_status}
                  </span>
                </div>
              ))}
            </div>
          </>
        )}

        {tab === "members" && (
          <>
            {boot.computers.length > 0 && (
              <form onSubmit={handleCreateAgent} className="p-3.5 border border-[#E4E1D9] rounded-[6px] space-y-2.5">
                <div className="text-[12.5px] font-semibold">Create Agent</div>
                <div className="grid grid-cols-2 gap-2">
                  <input
                    value={agName}
                    onChange={(e) => setAgName(e.target.value)}
                    placeholder="Display name (e.g. Lin)"
                    className="h-8 px-2.5 rounded border border-[#D5D1C7] text-[13px] outline-none"
                    required
                  />
                  <select
                    value={agDriver}
                    onChange={(e) => setAgDriver(e.target.value as "builtin" | "acp")}
                    className="h-8 px-2 rounded border border-[#D5D1C7] text-[13px] bg-white"
                  >
                    <option value="builtin">Builtin (Go Harness)</option>
                    <option value="acp">ACP Driver</option>
                  </select>
                </div>
                <input
                  value={agRole}
                  onChange={(e) => setAgRole(e.target.value)}
                  placeholder="Role description"
                  className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] text-[13px] outline-none"
                />
                <button type="submit" className="h-7 px-3 rounded bg-[#161614] text-white text-[12px] font-medium">
                  Create Agent
                </button>
              </form>
            )}
            <div className="border border-[#E4E1D9] rounded-[6px] divide-y divide-[#E4E1D9]">
              {boot.members.map((m) => (
                <div key={m.id} className="h-10 px-3 flex items-center justify-between text-[13px]">
                  <span className="flex items-center gap-2">
                    <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" />
                    <span className="font-medium">{m.display_name}</span>
                    <span className="text-[11.5px] text-[#8A867D]">{m.kind}</span>
                  </span>
                  {m.kind === "agent" ? (
                    <button
                      onClick={() => onInspectAgent(m.id)}
                      className="text-[12px] text-[#45433E] hover:text-[#161614] underline decoration-[#D5D1C7]"
                    >
                      Inspect
                    </button>
                  ) : (
                    <span className="text-[12px] text-[#8A867D]">{m.access_level}</span>
                  )}
                </div>
              ))}
            </div>
          </>
        )}

        {tab === "settings" && (
          <div className="space-y-4 text-[13px]">
            <div className="grid grid-cols-[120px_1fr] gap-y-2 border border-[#E4E1D9] rounded-[6px] p-3.5">
              <span className="text-[#6E6B64]">Space</span>
              <span className="font-medium">{boot.space.name}</span>
              <span className="text-[#6E6B64]">Slug</span>
              <span className="font-mono-num">{boot.space.slug}</span>
              <span className="text-[#6E6B64]">Account</span>
              <span>{boot.current_member.display_name} ({boot.current_member.access_level})</span>
            </div>
            <button
              onClick={onLogout}
              className="h-8 px-3 rounded border border-[#D5D1C7] hover:bg-[#FDF1EE] hover:text-[#A83224] text-[12.5px] font-medium"
            >
              Sign out
            </button>
          </div>
        )}
      </div>
    </section>
  );
}
