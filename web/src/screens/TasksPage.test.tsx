import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAppRouter } from "../router";
import type {Message, Run, Task, ThreadRead, ThreadReference} from "../api/types";

const ownerId = "019c0000-0000-7000-8000-000000000002";
const spaceId = "019c0000-0000-7000-8000-000000000001";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Task work index", () => {
  it("orders open statuses, filters history, and links to the Source Thread", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([]);
      if (path.endsWith("/tasks")) return json([
        task("todo", "Design claim flow", 17),
        task("in_progress", "Wire Agent CLI", 18, "Lin"),
        task("in_review", "Review Web contract", 19, "Reviewer"),
        task("done", "Ship old slice", 20, "Lin"),
      ]);
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute("/s/sumi-lab/tasks");

    expect(await screen.findByRole("heading", { name: "Tasks", level: 1 })).toBeVisible();
    const tasksView = within(screen.getByRole("heading", { name: "Tasks", level: 1 }).closest(".tasks-workspace")!);
    expect(await tasksView.findByText(/Review Web contract/)).toBeVisible();
    expect(tasksView.getByRole("link", { name: /Review Web contract/ })).toBeVisible();
    expect(tasksView.getByRole("link", { name: /Wire Agent CLI/ })).toBeVisible();
    expect(tasksView.getByRole("link", { name: /Design claim flow/ })).toBeVisible();
    expect(tasksView.queryByText(/Ship old slice/)).not.toBeInTheDocument();
    expect(within(tasksView.getByRole("link", { name: /Design claim flow/ }).closest(".task-row")! as HTMLElement).getByRole("link", { name: "Source: #general" })).toHaveAttribute("href", "/s/sumi-lab/channels/general#message-message-todo");
    fireEvent.click(tasksView.getByRole("button", { name: /^Done/ }));
    expect(await tasksView.findByText(/Ship old slice/)).toBeVisible();
    expect(tasksView.queryByText(/Design claim flow/)).not.toBeInTheDocument();
    fireEvent.click(tasksView.getByRole("button", { name: /^Closed/ }));
    expect(await tasksView.findByText("No closed Tasks.")).toBeVisible();
  });

  it("describes Task activity in plain words without internal run state", async () => {
    const value = task("in_progress", "Rebuild Task detail", 21, "Lin");
    value.current_run = run("working", value.source_thread);
    value.recent_runs = [run("yielded", value.source_thread)];
    value.session_continuity = { state: "warm", generation: 2 };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`) && !init?.method) return json(value);
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    expect(await screen.findByRole("heading", { name: value.title })).toBeVisible();
    const activity = within(screen.getByRole("complementary", { name: "Task activity" }));
    expect(activity.getByText(/is working on this/)).toBeVisible();
    expect(activity.getByText(/paused/)).toBeVisible();
    expect(screen.queryByText(/Generation|continuity|Run\b/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Reset continuity/i })).not.toBeInTheDocument();
  });

  it("asks for the result before finishing and sends the typed text", async () => {
    const value = task("in_review", "Review Web contract", 19, "Lin");
    const requests: Array<{ path: string; body: unknown }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`) && !init?.method) return json(value);
      if (path.endsWith(`/threads/${value.source_thread.id}`)) return json(threadRead(value.source_thread.id, [reply("agent", "Lin", "Done: the contract is documented.")]));
      if (path.endsWith(`/tasks/${value.id}/done`)) {
        requests.push({ path, body: JSON.parse(String(init?.body)) });
        return json({ ...value, status: "done", finished_at: "2026-07-28T11:00:00Z" });
      }
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    fireEvent.click(await screen.findByRole("button", { name: "Mark done" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByRole("heading", { name: "Finish !19?" })).toBeVisible();
    expect(dialog.getByText("This becomes the Task result.")).toBeVisible();
    const field = dialog.getByLabelText("Result") as HTMLTextAreaElement;
    expect(field.value).toBe("Done: the contract is documented.");
    fireEvent.change(field, { target: { value: "None: the contract is documented in #general." } });
    fireEvent.click(dialog.getByRole("button", { name: "Mark done" }));

    await vi.waitFor(() => expect(requests).toHaveLength(1));
    expect(requests[0].body).toEqual({ result_thread_id: value.source_thread.id, result_markdown: "None: the contract is documented in #general." });
  });

  it("shows the assignee's latest reply while review is pending", async () => {
    const value = task("in_review", "Review Web contract", 19, "Lin");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`)) return json(value);
      if (path.endsWith(`/threads/${value.source_thread.id}`)) {
        return json(threadRead(value.source_thread.id, [
          reply("agent", "Lin", "Ready for review: the schema is documented."),
          reply("human", "Ada", "Reading it now."),
          reply("human", "Ada", "One more question."),
          reply("human", "Ada", "And another."),
        ]));
      }
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    expect(await screen.findByRole("heading", { name: "Latest from Lin" })).toBeVisible();
    expect(screen.getAllByText("Ready for review: the schema is documented.")).toHaveLength(1);
  });

  it("posts the change request reason before moving the Task back", async () => {
    const value = task("in_review", "Review Web contract", 19, "Lin");
    const calls: Array<{ path: string; body?: unknown; method?: string }> = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`) && !init?.method) return json(value);
      if (path.endsWith(`/threads/${value.source_thread.id}/messages`) && init?.method === "POST") {
        calls.push({ path, body: JSON.parse(String(init.body)), method: init.method });
        return json(reply("human", "Ada", "@Lin Cover the retry path."), 201);
      }
      if (path.endsWith(`/threads/${value.source_thread.id}`)) return json(threadRead(value.source_thread.id, []));

      if (path.endsWith(`/tasks/${value.id}/request-changes`)) {
        calls.push({ path, method: init?.method });
        return json({ ...value, status: "in_progress" });
      }
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    fireEvent.click(await screen.findByRole("button", { name: "Request changes" }));
    expect(screen.getByRole("heading", { name: "What needs to change?" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Send request" })).toBeDisabled();
    expect(screen.getByPlaceholderText("Tell Lin what to change.")).toHaveFocus();
    expect(screen.getByText("Add one sentence on what to change.")).toBeVisible();
    fireEvent.change(screen.getByPlaceholderText("Tell Lin what to change."), { target: { value: "Cover the retry path." } });
    fireEvent.click(screen.getByRole("button", { name: "Send request" }));

    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[0].body).toMatchObject({
      body_markdown: "@Lin Cover the retry path.",
      mentions: ["agent"],
    });
    expect(calls[1].path).toContain("/request-changes");
  });

  it("retries only the status change after the reason was posted", async () => {
    const value = task("in_review", "Review Web contract", 19, "Lin");
    const calls: string[] = [];
    let statusAttempts = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`) && !init?.method) return json(value);
      if (path.endsWith(`/threads/${value.source_thread.id}/messages`) && init?.method === "POST") {
        calls.push("reply");
        return json(reply("human", "Ada", "@Lin Wrap the retry."), 201);
      }
      if (path.endsWith(`/threads/${value.source_thread.id}`)) return json(threadRead(value.source_thread.id, []));
      if (path.endsWith(`/tasks/${value.id}/request-changes`)) {
        statusAttempts += 1;
        calls.push("status");
        if (statusAttempts === 1) throw new Error("offline");
        return json({ ...value, status: "in_progress" });
      }
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    fireEvent.click(await screen.findByRole("button", { name: "Request changes" }));
    fireEvent.change(screen.getByPlaceholderText("Tell Lin what to change."), { target: { value: "Wrap the retry." } });
    fireEvent.click(screen.getByRole("button", { name: "Send request" }));

    expect(await screen.findByText("Reason sent. Status unchanged — retry to move it back to In progress.")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await vi.waitFor(() => expect(statusAttempts).toBe(2));
    expect(calls).toEqual(["reply", "status", "status"]);
  });

  it("offers Request changes only with an assignee and a readable Source Thread", async () => {
    const unreadable = task("in_review", "Review Web contract", 19, "Lin");
    const current = unreadable;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${current.id}`)) return json(current);
      if (path.endsWith(`/threads/${current.source_thread.id}`)) {
        return new Response(JSON.stringify({ error: { code: "permission_denied", message: "denied", retryable: false } }), { status: 403 });
      }
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${unreadable.id}`);

    expect(await screen.findByText("You cannot see the conversation this Task came from.")).toBeVisible();
    await vi.waitFor(() =>
      expect(screen.queryByRole("button", { name: "Request changes" })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Mark done" })).toBeVisible();
  });

  it("labels a DM Source Thread without a channel link", async () => {
    const value = task("in_progress", "DM Task", 21, "Lin");
    value.source_thread = { ...thread("source", 21), channel_slug: null };
    value.current_run = run("working", value.source_thread);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const shell = shellResponse(path);
      if (shell) return shell;
      if (path.endsWith("/agents")) return json([{ member_id: "agent", name: "Lin", desired_lifecycle: "active" }]);
      if (path.endsWith(`/tasks/${value.id}`) && !init?.method) return json(value);
      throw new Error(`Unexpected request: ${path}`);
    }));

    renderRoute(`/s/sumi-lab/tasks/${value.id}`);

    expect(await screen.findByRole("heading", { name: "DM Task" })).toBeVisible();
    expect(screen.getByText("!21 · Direct message")).toBeVisible();
    expect(screen.queryAllByRole("link", { name: /#general/ })).toHaveLength(0);
  });
});

function task(status: "todo" | "in_progress" | "in_review" | "done" | "closed", title: string, seq: number, assignee?: string): Task {
  return {
    id: `task-${status}`,
    seq,
    space_id: spaceId,
    title,
    status,
    creator_member_id: ownerId,
    creator_name: "Ada",
    assignee_agent_member_id: assignee ? "agent" : undefined,
    assignee_name: assignee,
    source_thread: thread("source", seq),
    related_threads: [],
    result_message: undefined,
    close_reason_code: undefined,
    close_reason_note: undefined,
    current_run: undefined,
    recent_runs: [],
    session_continuity: { state: "cold" },
    runtime_issue_code: undefined,
    created_at: "2026-07-28T00:00:00Z",
    updated_at: `2026-07-28T0${seq % 9}:00:00Z`,
    finished_at: status === "done" || status === "closed" ? "2026-07-28T10:00:00Z" : undefined,
  };
}

function reply(author: "agent" | "human", name: string, body: string): Message {
  return {
    id: `reply-${body.slice(0, 8)}`,
    seq: 0,
    channel_id: "channel",
    thread_id: "thread-19",
    placement: "reply",
    author: { id: author === "agent" ? "agent" : ownerId, kind: author, display_name: name },
    content: { type: "text", body_markdown: body },
    mentions: [],
    mention_all: false,
    attachments: [],
    attention_failures: [],
    task_refs: [],
    reply_count: 0,
    latest_replies: [],
    created_at: "2026-07-28T05:00:00Z",
  };
}

function threadRead(id: string, replies: Message[]): ThreadRead {
  return {
    thread_id: id,
    channel_id: "channel",
    snapshot_channel_seq: 12,
    root: reply("human", "Ada", "Please review the contract."),
    replies,
    is_following: false,
    task: undefined,
    task_relation: "source",
  };
}

function thread(relation: "source" | "related", seq: number): ThreadReference {
  return { id: `thread-${seq}`, channel_id: "channel", channel_slug: "general", root_message_id: seq === 17 ? "message-todo" : `message-${seq}`, root_message_seq: seq, relation };
}

function run(status: "working" | "yielded", focus: ThreadReference): Run {
  return { id: `run-${status}`, task_id: "task", agent_member_id: "agent", agent_name: "Lin", focus, status, outcome: status === "yielded" ? "yielded" : undefined, started_at: "2026-07-28T01:00:00Z" };
}

function shellResponse(path: string): Response | undefined {
  if (path.includes("/spaces/by-slug/")) return json({ id: spaceId, name: "Sumi Lab", slug: "sumi-lab", accent: "#F0602F", owner_member_id: ownerId, current_member_id: ownerId, general_channel_id: "channel" });
  if (path === "/api/v1/auth/me") return json({ id: "user", display_name: "Ada", email: "ada@example.test" });
  if (path.endsWith("/channels")) return json({ can_create: true, channels: [] });
  if (path.endsWith("/dms")) return json([]);
  if (path.endsWith("/members")) return json([{ id: ownerId, kind: "human", display_name: "Ada", access_level: "owner", permissions: [] }]);
  return undefined;
}

function renderRoute(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>);
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}
