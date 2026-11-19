import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { X } from "lucide-react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  api,
  formatClock,
  type BootstrapData,
  type Member,
  type Permission,
  type TaskStatus,
} from "../api";
import { MemberAvatar, RuntimeDot } from "./Identity";

export type DetailTarget =
  | { kind: "task"; taskID: string }
  | { kind: "thread"; threadConvID: string; rootMessageID: string; parentSlug: string }
  | { kind: "agent"; memberID: string };

const PERMISSIONS: Permission[] = ["agent.create", "channel.create", "channel.manage_members"];

export function DetailPane({
  spaceSlug,
  boot,
  target,
  onClose,
  onSelectTarget,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  target: DetailTarget;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
}) {
  if (target.kind === "task") {
    return <TaskDetail spaceSlug={spaceSlug} boot={boot} taskID={target.taskID} onClose={onClose} />;
  }
  if (target.kind === "thread") {
    return (
      <ThreadDetail
        spaceSlug={spaceSlug}
        threadConvID={target.threadConvID}
        parentSlug={target.parentSlug}
        onClose={onClose}
      />
    );
  }
  return (
    <AgentDetail
      spaceSlug={spaceSlug}
      boot={boot}
      memberID={target.memberID}
      onClose={onClose}
      onSelectTarget={onSelectTarget}
    />
  );
}

function TaskDetail({
  spaceSlug,
  boot,
  taskID,
  onClose,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  taskID: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [reply, setReply] = useState("");
  const [sending, setSending] = useState(false);

  const taskQ = useQuery({
    queryKey: ["task", spaceSlug, taskID],
    queryFn: () => api.getTask(spaceSlug, taskID).then((r) => r.task),
  });
  const task = taskQ.data ?? boot.tasks.find((t) => t.id === taskID);

  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, task?.conversation_id],
    queryFn: () => api.listMessages(spaceSlug, task!.conversation_id!, true),
    enabled: Boolean(task?.conversation_id),
  });

  function refresh() {
    void qc.invalidateQueries({ queryKey: ["task", spaceSlug, taskID] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    if (task?.conversation_id) {
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, task.conversation_id] });
    }
  }

  async function handleStatus(status: TaskStatus) {
    if (!task) return;
    await api.updateTaskStatus(spaceSlug, task.id, status);
    refresh();
  }

  async function handleClaim() {
    if (!task) return;
    await api.claimTask(spaceSlug, task.id);
    refresh();
  }

  async function handleAssign(memberID: string) {
    if (!task) return;
    await api.assignTask(spaceSlug, task.id, memberID);
    refresh();
  }

  async function handleSend(e: FormEvent) {
    e.preventDefault();
    if (!task || !reply.trim()) return;
    setSending(true);
    try {
      let convID = task.conversation_id;
      if (!convID) {
        const ensured = await api.ensureTaskConv(spaceSlug, task.id);
        convID = ensured.task.conversation_id;
      }
      if (convID) {
        await api.sendMessage(spaceSlug, convID, reply.trim());
        setReply("");
        void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, convID] });
      }
      refresh();
    } finally {
      setSending(false);
    }
  }

  if (!task) {
    return (
      <aside className="bg-[#FAF9F6] border-l border-[#DCD9D0] flex items-center justify-center text-[12.5px] text-[#8A867D]">
        Loading task…
      </aside>
    );
  }

  return (
    <aside className="bg-[#FAF9F6] border-l border-[#DCD9D0] flex flex-col min-h-0">
      <header className="h-11 px-3.5 bg-white border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <span className="font-mono-num text-[13px] font-semibold text-[#161614]">#{task.number}</span>
          <span className="text-[#D5D1C7]">/</span>
          <span className="text-[12px] text-[#6E6B64] truncate">
            {task.channel_slug ? `#${task.channel_slug}` : "Direct message"}
          </span>
        </div>
        <button onClick={onClose} className="w-6 h-6 rounded hover:bg-[#F4F2EC] text-[#6E6B64] flex items-center justify-center" aria-label="Close">
          <X className="w-3.5 h-3.5" />
        </button>
      </header>

      {/* Structured 2-column Key-Value Task Spec */}
      <div className="p-3.5 bg-white border-b border-[#E4E1D9] space-y-2.5 shrink-0">
        <h2 className="text-[14px] font-semibold text-[#161614] leading-snug">{task.title}</h2>
        {task.description && <p className="text-[12.5px] text-[#45433E] leading-relaxed">{task.description}</p>}
        <div className="grid grid-cols-[76px_1fr] gap-y-1.5 items-center text-[12px] pt-1.5 border-t border-[#ECEAE3]">
          <span className="text-[#6E6B64]">Status</span>
          <div>
            <select
              value={task.status}
              onChange={(e) => void handleStatus(e.target.value as TaskStatus)}
              className="h-6 px-1.5 rounded border border-[#D5D1C7] bg-white text-[#161614] font-medium text-[12px] outline-none"
            >
              <option value="todo">To Do</option>
              <option value="in_progress">In Progress</option>
              <option value="in_review">In Review</option>
              <option value="done">Done</option>
              <option value="closed">Closed</option>
            </select>
          </div>

          <span className="text-[#6E6B64]">Assignee</span>
          <div className="flex items-center gap-2">
            <select
              value={task.assignee_member_id ?? ""}
              onChange={(e) => void handleAssign(e.target.value)}
              className="h-6 px-1.5 rounded border border-[#D5D1C7] bg-white text-[#161614] text-[12px] outline-none max-w-[160px]"
            >
              <option value="">Unassigned</option>
              {boot.members.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.display_name} ({m.kind})
                </option>
              ))}
            </select>
            {task.assignee_member_id !== boot.current_member.id && task.status !== "done" && task.status !== "closed" && (
              <button
                onClick={() => void handleClaim()}
                className="h-6 px-2 rounded bg-[#161614] text-white text-[11.5px] font-medium hover:bg-[#2F2E2B]"
              >
                Claim
              </button>
            )}
          </div>

          <span className="text-[#6E6B64]">Revision</span>
          <span className="font-mono-num text-[#6E6B64]">rev {task.revision}</span>
        </div>
      </div>

      <div className="h-7 px-3.5 bg-[#F4F2EC] border-b border-[#E4E1D9] flex items-center justify-between text-[11.5px] font-medium text-[#6E6B64] shrink-0">
        <span>Discussion</span>
        <span className="font-mono-num">{msgsQ.data?.messages.length ?? task.message_count} messages</span>
      </div>

      <div className="flex-1 overflow-y-auto scroll-thin p-3.5 space-y-3 bg-white">
        {!task.conversation_id || (msgsQ.data?.messages.length ?? 0) === 0 ? (
          <div className="py-10 text-center text-[12.5px] text-[#8A867D]">
            No messages yet. Replying creates #{task.number}'s conversation.
          </div>
        ) : (
          msgsQ.data!.messages.map((m) =>
            m.kind === "action" ? (
              <div key={m.id} className="text-[11.5px] text-[#6E6B64] flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full bg-[#0F8B78]" />
                <span>
                  <strong className="text-[#161614] font-medium">{m.author_display_name}</strong>{" "}
                  {describeAction(m.action_kind, m.payload_json)}
                </span>
                <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(m.created_at)}</span>
              </div>
            ) : (
              <article key={m.id} className="flex items-start gap-2.5">
                <MemberAvatar id={m.author_member_id} name={m.author_display_name ?? "?"} kind={m.author_kind ?? "human"} size="sm" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-baseline gap-2">
                    <span className="text-[12.5px] font-semibold text-[#161614]">{m.author_display_name}</span>
                    <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(m.created_at)}</span>
                  </div>
                  <div className="mt-0.5 text-[13px] leading-relaxed text-[#161614] prose prose-sm max-w-none">
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.body_markdown}</ReactMarkdown>
                  </div>
                </div>
              </article>
            ),
          )
        )}
      </div>

      <form onSubmit={handleSend} className="border-t border-[#E4E1D9] bg-white p-2.5 flex items-center gap-2 shrink-0">
        <input
          value={reply}
          onChange={(e) => setReply(e.target.value)}
          placeholder={`Reply in #${task.number}…`}
          className="flex-1 h-7 px-2 text-[13px] outline-none placeholder:text-[#8A867D]"
        />
        <button
          disabled={sending || !reply.trim()}
          type="submit"
          className="h-7 px-2.5 rounded-[5px] bg-[#161614] text-white text-[12px] font-medium disabled:opacity-40"
        >
          Send
        </button>
      </form>
    </aside>
  );
}

function ThreadDetail({
  spaceSlug,
  threadConvID,
  parentSlug,
  onClose,
}: {
  spaceSlug: string;
  threadConvID: string;
  parentSlug: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [reply, setReply] = useState("");
  const [following, setFollowing] = useState(true);
  const msgsQ = useQuery({
    queryKey: ["messages", spaceSlug, threadConvID],
    queryFn: () => api.listMessages(spaceSlug, threadConvID, true),
  });

  async function toggleFollow() {
    const next = !following;
    await api.setFollowing(spaceSlug, threadConvID, next);
    setFollowing(next);
  }

  async function handleSend(e: FormEvent) {
    e.preventDefault();
    if (!reply.trim()) return;
    await api.sendMessage(spaceSlug, threadConvID, reply.trim());
    setReply("");
    void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, threadConvID] });
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  return (
    <aside className="bg-[#FAF9F6] border-l border-[#DCD9D0] flex flex-col min-h-0">
      <header className="h-11 px-3.5 bg-white border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2">
          <span className="text-[13.5px] font-semibold text-[#161614]">Thread</span>
          <span className="text-[#D5D1C7]">/</span>
          <span className="text-[12px] text-[#6E6B64]">#{parentSlug}</span>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => void toggleFollow()}
            className="h-6 px-2 rounded border border-[#E4E1D9] hover:bg-[#F4F2EC] text-[11.5px] font-medium text-[#161614]"
          >
            {following ? "Following" : "Follow"}
          </button>
          <button onClick={onClose} className="w-6 h-6 rounded hover:bg-[#F4F2EC] text-[#6E6B64] flex items-center justify-center" aria-label="Close">
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      </header>

      <div className="flex-1 overflow-y-auto scroll-thin p-3.5 space-y-3 bg-white">
        {(msgsQ.data?.messages ?? []).map((m) => (
          <article key={m.id} className="flex items-start gap-2.5">
            <MemberAvatar id={m.author_member_id} name={m.author_display_name ?? "?"} kind={m.author_kind ?? "human"} size="sm" />
            <div className="min-w-0 flex-1">
              <div className="flex items-baseline gap-2">
                <span className="text-[12.5px] font-semibold">{m.author_display_name}</span>
                <span className="font-mono-num text-[11px] text-[#8A867D]">{formatClock(m.created_at)}</span>
              </div>
              <div className="mt-0.5 text-[13px] text-[#161614] prose prose-sm max-w-none">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{m.body_markdown}</ReactMarkdown>
              </div>
            </div>
          </article>
        ))}
      </div>

      <form onSubmit={handleSend} className="border-t border-[#E4E1D9] bg-white p-2.5 flex items-center gap-2 shrink-0">
        <input
          value={reply}
          onChange={(e) => setReply(e.target.value)}
          placeholder="Reply in thread…"
          className="flex-1 h-7 px-2 text-[13px] outline-none placeholder:text-[#8A867D]"
        />
        <button disabled={!reply.trim()} type="submit" className="h-7 px-2.5 rounded-[5px] bg-[#161614] text-white text-[12px] font-medium disabled:opacity-40">
          Send
        </button>
      </form>
    </aside>
  );
}

function AgentDetail({
  spaceSlug,
  boot,
  memberID,
  onClose,
  onSelectTarget,
}: {
  spaceSlug: string;
  boot: BootstrapData;
  memberID: string;
  onClose: () => void;
  onSelectTarget: (t: DetailTarget) => void;
}) {
  const qc = useQueryClient();
  const member: Member | undefined = boot.members.find((m) => m.id === memberID);
  if (!member || !member.agent) return null;

  const comp = boot.computers.find((c) => c.id === member.agent?.computer_id);
  const canGovern = boot.current_member.access_level === "owner" || boot.current_member.access_level === "admin";
  const perms = new Set(member.permissions ?? []);
  const focusTask = boot.tasks.find((t) => t.conversation_id === member.agent?.focus_conversation_id);

  async function togglePerm(p: Permission) {
    await api.setPermission(spaceSlug, member!.id, p, !perms.has(p));
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  async function control(action: "restart" | "reset_session") {
    await api.controlAgent(spaceSlug, member!.id, action);
    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
  }

  return (
    <aside className="bg-white border-l border-[#DCD9D0] flex flex-col min-h-0">
      <header className="h-11 px-3.5 border-b border-[#E4E1D9] flex items-center justify-between shrink-0">
        <div className="flex items-center gap-2">
          <MemberAvatar id={member.id} name={member.display_name} kind="agent" size="sm" />
          <span className="text-[13.5px] font-semibold text-[#161614]">{member.display_name}</span>
          <span className="px-1.5 py-0.2 rounded bg-[#F1EFEA] text-[11px] text-[#5C5A54]">Agent</span>
        </div>
        <button onClick={onClose} className="w-6 h-6 rounded hover:bg-[#F4F2EC] text-[#6E6B64] flex items-center justify-center" aria-label="Close">
          <X className="w-3.5 h-3.5" />
        </button>
      </header>

      <div className="flex-1 overflow-y-auto scroll-thin divide-y divide-[#E4E1D9] text-[12.5px]">
        <div className="p-3.5 space-y-2.5">
          <div className="grid grid-cols-[96px_1fr] gap-y-2 items-center">
            <span className="text-[#6E6B64]">State</span>
            <span className="inline-flex items-center gap-1.5 font-medium text-[#161614]">
              <RuntimeDot state={member.agent.runtime_state} />
              <span>{member.agent.runtime_state}</span>
            </span>
            <span className="text-[#6E6B64]">Focus</span>
            <span>
              {focusTask ? (
                <button
                  onClick={() => onSelectTarget({ kind: "task", taskID: focusTask.id })}
                  className="font-mono-num underline decoration-[#D5D1C7]"
                >
                  task #{focusTask.number}
                </button>
              ) : (
                <span className="text-[#8A867D]">—</span>
              )}
            </span>
            <span className="text-[#6E6B64]">Computer</span>
            <span>{comp ? `${comp.name} · ${comp.connection_status}` : "unassigned"}</span>
            <span className="text-[#6E6B64]">Driver</span>
            <span>{member.agent.driver_kind} · 1 active session</span>
          </div>
          {canGovern && (
            <div className="flex items-center gap-2 pt-2">
              <button
                onClick={() => void control("restart")}
                className="flex-1 h-7 rounded border border-[#D5D1C7] hover:bg-[#F4F2EC] text-[12px] font-medium"
              >
                Restart
              </button>
              <button
                onClick={() => void control("reset_session")}
                className="flex-1 h-7 rounded border border-[#D5D1C7] hover:bg-[#F4F2EC] text-[12px] font-medium"
              >
                Reset session
              </button>
            </div>
          )}
        </div>

        <div className="p-3.5 space-y-1.5">
          <div className="text-[11.5px] font-semibold text-[#6E6B64]">Role</div>
          <p className="text-[#45433E] whitespace-pre-wrap">{member.agent.role_text || "No role defined."}</p>
        </div>

        <div className="p-3.5 space-y-2">
          <div className="text-[11.5px] font-semibold text-[#6E6B64]">Permissions</div>
          <div className="divide-y divide-[#ECEAE3] border border-[#E4E1D9] rounded-[5px]">
            {PERMISSIONS.map((p) => (
              <label key={p} className="h-8 px-2.5 flex items-center justify-between cursor-pointer">
                <span className="font-mono-num text-[12px]">{p}</span>
                <input
                  type="checkbox"
                  disabled={!canGovern}
                  checked={perms.has(p)}
                  onChange={() => void togglePerm(p)}
                  className="accent-[#161614]"
                />
              </label>
            ))}
          </div>
        </div>
      </div>
    </aside>
  );
}

function describeAction(kind?: string, raw?: string): string {
  try {
    const p = raw ? JSON.parse(raw) : {};
    if (kind === "agent_created") return `created agent ${p.display_name ?? ""}`;
    if (kind === "channel_created") return `created #${p.slug ?? ""}`;
    if (kind === "task_event") {
      if (p.op === "claimed") return `claimed #${p.task_number}`;
      if (p.op === "unclaimed") return `unclaimed #${p.task_number}`;
      if (p.op === "assigned") return `assigned #${p.task_number} to ${p.assignee_name ?? "nobody"}${p.reason ? `: ${p.reason}` : ""}`;
      if (p.op === "status_changed") return `moved #${p.task_number} ${p.from} → ${p.to}`;
      if (p.op === "amended") return `amended #${p.task_number} (rev ${p.revision})`;
    }
  } catch {
    // ignore malformed payload
  }
  return kind ?? "event";
}
