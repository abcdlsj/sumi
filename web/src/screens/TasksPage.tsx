import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { ArrowUpRight, Check, ListTodo, MessageSquare, X, XCircle } from "lucide-react";
import { useState } from "react";

import { v7 as uuidv7 } from "uuid";

import {closeTask, completeTask, createThreadReply, getTask, listAgents, listTasks, readThread, requestTaskChanges, startTask} from "../api/client";
import type {Message, Member, Run, Task, TaskStatus, ThreadRead, ThreadReference} from "../api/types";
import { DialogFrame } from "../components/DialogFrame";
import { EmptyState } from "../components/EmptyState";
import { SpacePage } from "../components/SpaceShell";
import { SumiSelect } from "../components/SumiSelect";
import { TaskStatusIcon } from "../components/taskStatusIcon";
import { DetailSkeleton, ListSkeleton } from "../components/Skeleton";
import { plainText } from "../markdownText";
import { formatDateTime, formatRecent } from "../time";
import { CompactMessage } from "../components/channel/MessageTimeline";
import { PixelIdentity } from "../components/PixelIdentity";

type TaskFilter = "all_open" | TaskStatus | "created_by_me";

const filters: Array<{ id: TaskFilter; label: string }> = [
  { id: "all_open", label: "All open" },
  { id: "todo", label: "To do" },
  { id: "in_progress", label: "In progress" },
  { id: "in_review", label: "In review" },
  { id: "done", label: "Done" },
  { id: "closed", label: "Closed" },
  { id: "created_by_me", label: "Created by me" },
];

const openOrder: Record<TaskStatus, number> = {
  in_review: 0,
  in_progress: 1,
  todo: 2,
  done: 3,
  closed: 4,
};

const statusLabels: Record<TaskStatus, string> = {
  todo: "To do",
  in_progress: "In progress",
  in_review: "In review",
  done: "Done",
  closed: "Closed",
};

const emptyFacts: Record<TaskFilter, string> = {
  all_open: "No open Tasks.",
  todo: "No Tasks to do.",
  in_progress: "No Tasks in progress.",
  in_review: "No Tasks in review.",
  done: "No completed Tasks.",
  closed: "No closed Tasks.",
  created_by_me: "You have not created any Tasks.",
};

export function TasksPage() {
  return (
    <SpacePage>
      {({ space }) => (
        <TaskList
          spaceId={space.id}
          spaceSlug={space.slug}
          currentMemberId={space.current_member_id}
        />
      )}
    </SpacePage>
  );
}

function TaskList({ spaceId, spaceSlug, currentMemberId }: { spaceId: string; spaceSlug: string; currentMemberId: string }) {
  const [filter, setFilter] = useState<TaskFilter>("all_open");
  const tasks = useQuery({ queryKey: ["tasks", spaceId], queryFn: () => listTasks(spaceId) });
  const matches = (task: Task, id: TaskFilter) => id === "all_open"
    ? !["done", "closed"].includes(task.status)
    : id === "created_by_me"
      ? task.creator_member_id === currentMemberId
      : task.status === id;
  const visible = (tasks.data ?? [])
    .filter((task) => matches(task, filter))
    .sort((left, right) => openOrder[left.status] - openOrder[right.status] || right.updated_at.localeCompare(left.updated_at));
  const groups = groupTasks(visible);

  return (
    <section className="tasks-workspace" aria-labelledby="tasks-heading">
      <header className="tasks-header">
        <div className="page-title"><h1 id="tasks-heading">Tasks</h1></div>
      </header>
      <nav className="task-filters" aria-label="Task filters">
        {filters.map((item) => {
          const count = tasks.data ? tasks.data.filter((task) => matches(task, item.id)).length : undefined;
          return (
            <button key={item.id} type="button" aria-pressed={filter === item.id} onClick={() => setFilter(item.id)}>
              {item.label}
              {count ? <span className="task-filter-count" aria-label={`, ${count}`}>{count}</span> : null}
            </button>
          );
        })}
      </nav>
      {tasks.isPending ? <ListSkeleton label="Loading Tasks" avatar={false} /> : null}
      {tasks.error ? <div className="route-status route-status--error" role="alert">Tasks unavailable. Retry from this page.</div> : null}
      {!tasks.isPending && !tasks.error && visible.length === 0 ? (
        <EmptyState
          icon={ListTodo}
          title={emptyFacts[filter]}
          action={filter === "all_open" ? (
            <Link className="command-button" to="/s/$spaceSlug/channels/$channelSlug" params={{ spaceSlug, channelSlug: "general" }}>
              Open #general
              <ArrowUpRight aria-hidden="true" />
            </Link>
          ) : undefined}
        >
          {filter === "all_open" ? "Hover any message in a Channel and choose Create Task to hand the work to an Agent." : null}
        </EmptyState>
      ) : null}
      {groups.length ? (
        <div className="task-groups">
          {groups.map((group) => (
            <section className="task-status-group" key={group.status} aria-labelledby={`task-group-${group.status}`}>
              <header className="task-status-group-heading">
                <h2 id={`task-group-${group.status}`}>{statusLabels[group.status]}</h2>
                <span>{group.tasks.length}</span>
              </header>
              <div className="task-list" role="list">
                {group.tasks.map((task) => <TaskRow key={task.id} task={task} spaceSlug={spaceSlug} />)}
              </div>
            </section>
          ))}
        </div>
      ) : null}
    </section>
  );
}

function TaskRow({ task, spaceSlug }: { task: Task; spaceSlug: string }) {
  return (
    <article className="task-row" role="listitem">
      <div className="task-row-status">
        <TaskStatusLabel status={task.status} />
      </div>
      <Link className="task-row-main" to="/s/$spaceSlug/tasks/$taskId" params={{ spaceSlug, taskId: task.id }}>
        <span className="task-row-reference">!{task.seq}</span>
        <strong>{task.title}</strong>
      </Link>
      <dl className="task-row-facts">
        <div><dt>Assignee</dt><dd><TaskAssignee task={task} /></dd></div>
        <div><dt>Source</dt><dd><ThreadLink thread={task.source_thread} spaceSlug={spaceSlug} label="Source" /></dd></div>
        <div><dt>Updated</dt><dd><time dateTime={task.updated_at}>{formatTaskTime(task.updated_at)}</time></dd></div>
      </dl>
    </article>
  );
}

function TaskAssignee({ task }: { task: Task }) {
  if (!task.assignee_name || !task.assignee_agent_member_id) return <span className="task-assignee-name task-assignee-name--none">Unassigned</span>;
  return (
    <span className="task-assignee-name">
      <PixelIdentity name={task.assignee_name} kind="agent" seed={task.assignee_agent_member_id} />
      {task.assignee_name}
    </span>
  );
}

function groupTasks(tasks: Task[]): Array<{ status: TaskStatus; tasks: Task[] }> {
  const groups = new Map<TaskStatus, Task[]>();
  for (const task of tasks) {
    const group = groups.get(task.status) ?? [];
    group.push(task);
    groups.set(task.status, group);
  }
  return [...groups.entries()].map(([status, groupedTasks]) => ({ status, tasks: groupedTasks }));
}

export function TaskDetailPage() {
  const { taskId } = useParams({ from: "/s/$spaceSlug/tasks/$taskId" });
  return (
    <SpacePage>
      {({ space, members }) => <TaskDetail taskId={taskId} spaceId={space.id} spaceSlug={space.slug} members={members} />}
    </SpacePage>
  );
}

function TaskDetail({ taskId, spaceId, spaceSlug, members }: { taskId: string; spaceId: string; spaceSlug: string; members: Member[] }) {
  const queryClient = useQueryClient();
  const task = useQuery({ queryKey: ["task", taskId], queryFn: () => getTask(taskId) });
  const agents = useQuery({ queryKey: ["agents", spaceId], queryFn: () => listAgents(spaceId) });
  const change = useMutation({
    mutationFn: async (operation: () => Promise<Task>) => operation(),
    meta: { success: (task) => `Task !${(task as Task).seq} · ${statusLabels[(task as Task).status]}.` },
    onSuccess: (updated) => {
      queryClient.setQueryData(["task", taskId], updated);
      void queryClient.invalidateQueries({ queryKey: ["tasks", updated.space_id] });
    },
  });
  const [closeOpen, setCloseOpen] = useState(false);
  const [closeReason, setCloseReason] = useState("invalid");
  const [closeNote, setCloseNote] = useState("");
  const [doneOpen, setDoneOpen] = useState(false);
  const [changesOpen, setChangesOpen] = useState(false);
  // Request changes posts its reason into the Source Thread, so the action exists only once that
  // Thread is readable. DESIGN: an unreadable Source Thread offers neither delivery strip nor action.
  const sourceThreadId = task.data?.source_thread.id;
  const sourceThread = useQuery({
    queryKey: ["thread", sourceThreadId],
    queryFn: () => readThread(sourceThreadId!),
    enabled: Boolean(sourceThreadId),
  });

  if (task.isPending) return <DetailSkeleton label="Loading Task" />;
  if (task.error || !task.data) return <div className="route-status route-status--error" role="alert">Task unavailable or outside your visible Threads.</div>;
  const value = task.data;
  const canRequestChanges = value.status === "in_review" && Boolean(value.assignee_agent_member_id) && sourceThread.isSuccess;

  function markDone(result: string) {
    const thread = value.result_thread;
    if (!thread) return;
    change.mutate(
      () =>
        completeTask(value.id, {
          result_thread_id: thread.id,
          result_markdown: result,
        }),
      { onSuccess: () => setDoneOpen(false) },
    );
  }
  function confirmClose() {
    change.mutate(() => closeTask(value.id, {
      reason: closeReason as "invalid" | "duplicate" | "not_needed" | "obsolete" | "other",
      note: closeNote.trim() || undefined,
    }), { onSuccess: () => setCloseOpen(false) });
  }

  return (
    <section className="task-detail" aria-labelledby="task-heading">
      <header className="task-detail-header">
        <div className="page-title">
          <h1 id="task-heading">{value.title}</h1>
          <p>!{value.seq} · {threadDisplayLabel(value.source_thread)}</p>
        </div>
        <div className="task-detail-header-facts">
          <div><span>Assignee</span><strong><TaskAssignee task={value} /></strong></div>
          <TaskStatusLabel status={value.status} />
        </div>
      </header>
      {value.runtime_issue_code ? <p className="inline-notice inline-notice--error" role="alert">This Task cannot continue right now. Check that the assigned Agent is still in the conversation it was asked in.</p> : null}
      {change.error ? <p className="inline-notice inline-notice--error" role="alert">Could not update this Task. Nothing was changed.</p> : null}
      <div className="task-detail-scroll">
        <div className="task-detail-primary">
        <TaskConversation task={value} spaceSlug={spaceSlug} members={members} />
        <section className="task-detail-section"><h2>Task facts</h2><dl className="detail-grid"><Field label="Created by" value={value.creator_name} /><Field label="Created" value={formatTaskTime(value.created_at)} /><Field label="Updated" value={formatTaskTime(value.updated_at)} /></dl>{value.status === "todo" && !value.assignee_agent_member_id ? (
          <div className="task-assignee"><span>Assignee</span><SumiSelect value="" onChange={(agentId) => change.mutate(() => startTask(value.id, agentId))} options={(agents.data ?? []).filter((agent) => agent.desired_lifecycle === "active").map((agent) => ({ value: agent.member_id, label: agent.name }))} ariaLabel="Assign Agent" disabled={change.isPending} /></div>
        ) : null}</section>
        {!value.finished_at ? (
          <section className="task-detail-section"><h2>Actions</h2>
            <div className="task-actions">
              {value.result_thread ? (
                <button className="command-button command-button--accent" type="button" disabled={!["in_progress", "in_review"].includes(value.status) || change.isPending} onClick={() => setDoneOpen(true)}><Check /> Mark done</button>
              ) : null}
              {canRequestChanges ? (
                <button className="command-button" type="button" aria-expanded={changesOpen} disabled={changesOpen} onClick={() => setChangesOpen(true)}><MessageSquare /> Request changes</button>
              ) : null}
            </div>
            {canRequestChanges && changesOpen ? <TaskRequestChanges task={value} close={() => setChangesOpen(false)} /> : null}
            {!value.result_thread ? <p className="task-actions-hint">You cannot see any Conversation linked to this Task, so its Result cannot be written here.</p> : null}
            {value.status === "todo" ? <p className="task-actions-hint">Assign an Agent to start. A Task can be marked done once it is in progress or in review.</p> : null}
            {!value.finished_at ? (
              <div className="task-actions-secondary">
                <button className="danger-button" type="button" disabled={change.isPending} onClick={() => setCloseOpen(true)}><XCircle /> Close Task…</button>
              </div>
            ) : null}
          </section>
        ) : null}
        {value.status === "done" ? <section className="task-detail-section"><h2>Result</h2>{value.result_message?.content.type === "text" ? <p className="task-result">{plainText(value.result_message.content.body_markdown)}</p> : <p>The result is not available.</p>}</section> : null}
        {value.status === "closed" ? <section className="task-detail-section"><h2>Close reason</h2><p>{closeReasonLabel(value.close_reason_code)}{value.close_reason_note ? ` — ${value.close_reason_note}` : ""}</p></section> : null}
        </div>
        <aside className="task-detail-sidebar" aria-label="Task activity">
        <section className="task-detail-section"><h2>Activity</h2><TaskActivity task={value} spaceSlug={spaceSlug} /></section>
        </aside>
      </div>
      {doneOpen ? (
        <MarkDoneDialog
          task={value}
          threadId={value.result_thread?.id ?? value.source_thread.id}
          pending={change.isPending}
          onCancel={() => setDoneOpen(false)}
          onConfirm={markDone}
        />
      ) : null}
      {closeOpen ? (
        <DialogFrame className="confirm-dialog" close={() => setCloseOpen(false)} labelId="close-task-title">
          <header>
            <h2 id="close-task-title">Close Task !{value.seq}</h2>
            <button className="icon-button" type="button" aria-label="Close Close Task" onClick={() => setCloseOpen(false)}><X /></button>
          </header>
          <form onSubmit={(event) => { event.preventDefault(); confirmClose(); }}>
            <label className="task-field">Reason<SumiSelect value={closeReason} onChange={setCloseReason} options={[{ value: "invalid", label: "Invalid" }, { value: "duplicate", label: "Duplicate" }, { value: "not_needed", label: "Not needed" }, { value: "obsolete", label: "Obsolete" }, { value: "other", label: "Other" }]} ariaLabel="Close reason" /></label>
            <label className="task-field">Note<input value={closeNote} onChange={(event) => setCloseNote(event.target.value)} /></label>
            <footer>
              <button className="command-button" type="button" onClick={() => setCloseOpen(false)}>Cancel</button>
              <button className="danger-button" type="submit" disabled={change.isPending}><XCircle /> Close Task</button>
            </footer>
          </form>
        </DialogFrame>
      ) : null}
    </section>
  );
}

/** The conversation that asked for the work: the first message and its latest replies. */
function TaskConversation({ task, spaceSlug, members }: { task: Task; spaceSlug: string; members: Member[] }) {
  const thread = task.source_thread;
  const read = useQuery({ queryKey: ["thread", thread.id], queryFn: () => readThread(thread.id) });
  const replies = read.data?.replies ?? [];
  const latest = replies.slice(-3);
  const latestFromAssignee = replies.filter((reply) => reply.author.id === task.assignee_agent_member_id).at(-1);
  return (
    <section className="task-detail-section task-conversation" aria-labelledby="task-conversation-heading">
      <header className="task-conversation-heading">
        <h2 id="task-conversation-heading">Request</h2>
        {thread.channel_slug ? (
          <Link className="compact-action" to="/s/$spaceSlug/channels/$channelSlug" params={{ spaceSlug, channelSlug: thread.channel_slug }} hash={`message-${thread.root_message_id}`}>
            <MessageSquare aria-hidden="true" /> Open Thread
          </Link>
        ) : null}
      </header>
      {read.isPending ? <ListSkeleton label="Loading request" rows={2} /> : null}
      {read.error ? <p className="task-conversation-empty">You cannot see the conversation this Task came from.</p> : null}
      {read.data ? (
        <div className="task-conversation-body">
          {task.status === "in_review" ? (
            <LatestFromAssignee
              task={task}
              latest={latestFromAssignee}
              alreadyShown={latest.some((reply) => reply.id === latestFromAssignee?.id)}
            />
          ) : null}
          <CompactMessage message={read.data.root} spaceSlug={spaceSlug} members={members} />
          {replies.length > latest.length ? <p className="task-conversation-more">{replies.length - latest.length} earlier {replies.length - latest.length === 1 ? "reply" : "replies"}</p> : null}
          {latest.map((reply) => <CompactMessage key={reply.id} message={reply} spaceSlug={spaceSlug} members={members} />)}
        </div>
      ) : null}
    </section>
  );
}

/** While review is pending, the assignee's last word sits at the top of the Request card. */
function LatestFromAssignee({ task, latest, alreadyShown }: { task: Task; latest: Message | undefined; alreadyShown: boolean }) {
  const [expanded, setExpanded] = useState(true);
  const name = task.assignee_name ?? "the Agent";
  // A reply the Request body already lists below needs no second copy.
  if (alreadyShown) return null;
  if (!latest) {
    return <p className="task-latest task-latest--empty">No reply from {name} yet.</p>;
  }
  const body = latest.content.type === "text" ? plainText(latest.content.body_markdown) : "";
  return (
    <div className="task-latest">
      <header>
        <h3>Latest from {name}</h3>
        <time dateTime={latest.created_at}>{formatRecent(latest.created_at)}</time>
      </header>
      <p className={expanded ? "task-latest-body" : "task-latest-body task-latest-body--clamped"}>{body || "The reply is not plain text."}</p>
      {body ? (
        <button className="task-latest-toggle" type="button" aria-expanded={expanded} onClick={() => setExpanded((open) => !open)}>
          {expanded ? "Show less" : "Show more"}
        </button>
      ) : null}
    </div>
  );
}

/** Finish asks for the result instead of writing a placeholder. */
function MarkDoneDialog({ task, threadId, pending, onCancel, onConfirm }: { task: Task; threadId: string; pending: boolean; onCancel: () => void; onConfirm: (result: string) => void }) {
  // The Result lands in the Thread the Server resolved for this actor; its latest reply is the prefill.
  const read = useQuery({ queryKey: ["thread", threadId], queryFn: () => readThread(threadId) });
  const [result, setResult] = useState<string | undefined>(undefined);
  const prefill = latestReplyText(read.data, task.assignee_agent_member_id);
  const value = result ?? prefill;
  return (
    <DialogFrame className="confirm-dialog" close={onCancel} labelId="finish-task-title">
      <header>
        <h2 id="finish-task-title">Finish !{task.seq}?</h2>
        <button className="icon-button" type="button" aria-label="Close Finish Task" onClick={onCancel}><X /></button>
      </header>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          onConfirm(value.trim());
        }}
      >
        <p>This becomes the Task result.</p>
        <label className="task-field">
          Result
          <textarea value={value} rows={4} onChange={(event) => setResult(event.target.value)} />
        </label>
        <footer>
          <button className="command-button" type="button" onClick={onCancel}>Cancel</button>
          <button className="command-button command-button--accent" type="submit" disabled={pending || !value.trim()}><Check /> Mark done</button>
        </footer>
      </form>
    </DialogFrame>
  );
}

/** Asks the assignee for another pass: a normal reply with a real mention, then the status change.
 *
 * The form sits below the action row, not inside it, so opening it does not reflow the buttons.
 */
function TaskRequestChanges({ task, close }: { task: Task; close: () => void }) {
  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");
  const [reasonSent, setReasonSent] = useState(false);
  const change = useMutation({
    mutationFn: async ({ reason, resent }: { reason: string; resent: boolean }) => {
      if (!resent) {
        await createThreadReply(
          task.source_thread.id,
          {
            body_markdown: `@${task.assignee_name} ${reason}`,
            mentions: task.assignee_agent_member_id ? [task.assignee_agent_member_id] : [],
            mention_all: false,
            attachment_ids: [],
          },
          uuidv7(),
        );
        setReasonSent(true);
      }
      return requestTaskChanges(task.id);
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(["task", updated.id], updated);
      void queryClient.invalidateQueries({ queryKey: ["tasks", updated.space_id] });
      void queryClient.invalidateQueries({ queryKey: ["thread", updated.source_thread.id] });
      close();
      setReason("");
      setReasonSent(false);
    },
  });

  const submit = () => {
    if (reason.trim()) change.mutate({ reason: reason.trim(), resent: reasonSent });
  };

  return (
    <>
      <form
        className="task-changes"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <h3>What needs to change?</h3>
        <textarea
          autoFocus
          value={reason}
          rows={3}
          placeholder={`Tell ${task.assignee_name} what to change.`}
          onChange={(event) => {
            setReason(event.target.value);
            // A new sentence is a new request: the reply must be posted again.
            setReasonSent(false);
          }}
          onKeyDown={(event) => {
            if (event.key === "Escape") close();
            if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
              event.preventDefault();
              submit();
            }
          }}
        />
        {reason.trim() ? null : <p className="task-changes-hint" role="status">Add one sentence on what to change.</p>}
        <div className="task-changes-actions">
          <button className="command-button command-button--accent" type="submit" disabled={!reason.trim() || change.isPending}>{change.isError && reasonSent ? "Retry" : "Send request"}</button>
          <button
            className="command-button"
            type="button"
            onClick={() => {
              close();
              setReason("");
              setReasonSent(false);
            }}
          >
            Cancel
          </button>
        </div>
      </form>
      {change.isError ? (
        <p className="inline-notice inline-notice--error" role="alert">{reasonSent ? "Reason sent. Status unchanged — retry to move it back to In progress." : "Could not post the reason. Nothing was changed."}</p>
      ) : null}
    </>
  );
}

/** The assignee's last plain-text reply, used to prefill the result. */
function latestReplyText(read: ThreadRead | undefined, assigneeId: string | null | undefined): string {
  if (!read || !assigneeId) return "";
  const reply = read.replies
    .filter((candidate) => candidate.author.id === assigneeId)
    .at(-1);
  return reply?.content.type === "text" ? plainText(reply.content.body_markdown) : "";
}

function TaskStatusLabel({ status }: { status: TaskStatus }) {
  return <span className={`task-status task-status--${status}`}><TaskStatusIcon status={status} /><span>{statusLabels[status]}</span></span>;
}
function threadDisplayLabel(thread: ThreadReference): string {
  return thread.channel_slug ? `#${thread.channel_slug}` : "Direct message";
}
function ThreadLink({ thread, spaceSlug, label }: { thread: ThreadReference; spaceSlug: string; label: string }) {
  const address = threadDisplayLabel(thread);
  if (!thread.channel_slug) {
    return <span className="task-source-link" aria-label={`${label}: ${address}`}>{address}</span>;
  }
  return <Link className="task-source-link" to="/s/$spaceSlug/channels/$channelSlug" params={{ spaceSlug, channelSlug: thread.channel_slug }} hash={`message-${thread.root_message_id}`} aria-label={`${label}: ${address}`}><span>{address}</span><ArrowUpRight aria-hidden="true" /></Link>;
}
const runOutcome: Record<Run["status"], string> = {
  dispatched: "Starting",
  working: "Working",
  completed: "Finished",
  yielded: "Paused",
  failed: "Stopped on an error",
  canceled: "Stopped",
};
/** Who is on the Task now and how earlier attempts ended, in plain words. */
function TaskActivity({ task, spaceSlug }: { task: Task; spaceSlug: string }) {
  const current = task.current_run;
  const past = task.recent_runs.filter((run) => run.id !== current?.id);
  if (!current && !past.length) return <p className="task-activity-empty">No one has worked on this yet.</p>;
  return (
    <ul className="task-activity">
      {current ? <ActivityItem runs={[current]} spaceSlug={spaceSlug} current /> : null}
      {groupRuns(past).map((runs) => <ActivityItem key={runs[0].id} runs={runs} spaceSlug={spaceSlug} />)}
    </ul>
  );
}

/** One attempt per row, unless the same Agent ended the same way in the same Thread again:
 *  eleven identical "finished" rows say less than one row that names the count. */
function groupRuns(runs: readonly Run[]): Run[][] {
  const groups: Run[][] = [];
  for (const run of runs) {
    const group = groups[groups.length - 1];
    const head = group?.[0];
    if (
      head &&
      head.agent_name === run.agent_name &&
      head.status === run.status &&
      head.focus.id === run.focus.id
    ) {
      group.push(run);
      continue;
    }
    groups.push([run]);
  }
  return groups;
}

function ActivityItem({ runs, spaceSlug, current = false }: { runs: Run[]; spaceSlug: string; current?: boolean }) {
  const run = runs[0];
  const when = run.finished_at ?? run.started_at;
  const repeats = runs.length > 1 ? `${runs.length} runs · ` : "";
  return (
    <li className={`task-activity-item task-activity-item--${run.status}${current ? " task-activity-item--current" : ""}`}>
      <span className="task-activity-dot" aria-hidden="true" />
      <div>
        <p><strong>{run.agent_name}</strong> {current ? (run.status === "dispatched" ? "is starting on this" : "is working on this") : runOutcome[run.status].toLowerCase()}</p>
        <small>
          {when ? <time dateTime={when}>{repeats}{formatTaskTime(when)}</time> : null}
          {run.focus.channel_slug ? <> · <ThreadLink thread={run.focus} spaceSlug={spaceSlug} label="Conversation" /></> : null}
        </small>
      </div>
    </li>
  );
}
function Field({ label, value }: { label: string; value: string }) { return <div><dt>{label}</dt><dd>{value}</dd></div>; }
function formatTaskTime(value: string) { return formatDateTime(value); }
function closeReasonLabel(reason: Task["close_reason_code"]) { return reason ? reason.replace("_", " ").replace(/^./, (value) => value.toUpperCase()) : "Unknown"; }
