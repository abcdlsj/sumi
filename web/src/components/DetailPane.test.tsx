import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearAgentTemplatesForTests, listAgentTemplates, saveAgentTemplate } from "../agentTemplates";
import { api, type BootstrapData, type Member, type Message, type Task } from "../api";
import { DetailPane } from "./DetailPane";

const boot = { members: [], tasks: [], computers: [], conversations: [], inbox: [], current_member: { access_level: "owner" } } as unknown as BootstrapData;

const lisi = { id: "m-lisi", space_id: "s", kind: "human" as const, display_name: "Lisi", access_level: "owner" as const, created_at: "2026-11-19T10:00:00Z" };
const sam = { id: "m-sam", space_id: "s", kind: "human" as const, display_name: "Sam", access_level: "member" as const, created_at: "2026-11-19T10:00:00Z" };
const lin = {
  id: "m-lin",
  space_id: "s",
  kind: "agent" as const,
  display_name: "Lin",
  access_level: "member" as const,
  agent: { role_text: "Backend engineer", driver_kind: "acp" as const, runtime_state: "idle" as const },
  created_at: "2026-11-19T10:00:00Z",
};
const roster = { ...boot, members: [lisi, lin, sam], current_member: lisi } as unknown as BootstrapData;

const task = {
  id: "t-14",
  space_id: "s",
  number: 14,
  channel_id: "conv-eng",
  channel_slug: "engineering",
  conversation_id: "conv-task",
  title: "Unified read watermark",
  description: "",
  status: "in_progress",
  creator_member_id: lisi.id,
  revision: 1,
  message_count: 1,
  created_at: "2026-11-19T10:12:00Z",
  updated_at: "2026-11-19T10:15:00Z",
} as Task;

function reply(seq: number, author: Member = sam, body = `reply ${seq}`): Message {
  return {
    id: `tr-${seq}`,
    space_id: "s",
    conversation_id: "th-1",
    seq,
    author_member_id: author.id,
    author_display_name: author.display_name,
    author_kind: author.kind,
    kind: "text",
    body_markdown: body,
    created_at: new Date(Date.UTC(2026, 10, 19, 10, seq)).toISOString(),
  };
}

function renderPane(target: Parameters<typeof DetailPane>[0]["target"], data: BootstrapData = roster) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={qc}>
      <DetailPane spaceSlug="s" boot={data} target={target} onClose={() => {}} onSelectTarget={() => {}} />
    </QueryClientProvider>,
  );
  return { ...view, qc };
}

function sizeStream(el: Element | null, height: number) {
  if (!el) throw new Error("stream not found");
  Object.defineProperty(el, "scrollHeight", { value: height, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: 100, configurable: true });
  return el as HTMLElement;
}

beforeEach(() => {
  vi.spyOn(api, "listConversationMembers").mockResolvedValue({ members: [lin, sam] });
  vi.spyOn(api, "sendMessage").mockResolvedValue({ message: reply(9, lisi, "sent") });
});

afterEach(() => {
  try {
    localStorage?.removeItem?.("sumi.pref.task_details_collapsed");
  } catch {
    // ignore unavailable storage in test environments
  }
  clearAgentTemplatesForTests();
  vi.restoreAllMocks();
});

describe("ThreadDetail", () => {
  it("starts from the server following state and toggles it", async () => {
    let following = false;
    vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages: [], read_seq: 0, following }));
    const setFollowing = vi.spyOn(api, "setFollowing").mockImplementation(async (_s, _c, next) => {
      following = next;
      return { ok: true };
    });
    renderPane({ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" });
    fireEvent.click(await screen.findByRole("button", { name: "Follow" }, { timeout: 3000 }));
    await waitFor(() => expect(setFollowing).toHaveBeenCalledWith("s", "th-1", true));
    expect(await screen.findByRole("button", { name: "Following" })).toBeTruthy();
  });

  it("completes mentions in the reply box without sending", async () => {
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [reply(1), reply(2)], read_seq: 0, following: true });
    renderPane({ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" });
    await screen.findByText("reply 2");

    const box = screen.getByRole("textbox", { name: "Reply in thread…" });
    fireEvent.change(box, { target: { value: "@" } });
    const list = await screen.findByRole("listbox", { name: "Mention candidates" });
    const options = within(list).getAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual([expect.stringContaining("Lin"), expect.stringContaining("Sam")]);

    fireEvent.keyDown(box, { key: "Enter" });
    expect((box as HTMLTextAreaElement).value).toBe("@Lin ");
    expect(api.sendMessage).not.toHaveBeenCalled();
  });

  it("follows new replies and offers a jump pill when parked", async () => {
    let messages = [reply(1), reply(2)];
    vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages, read_seq: 0, following: true }));
    const { container, qc } = renderPane({ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" });
    const stream = sizeStream(container.querySelector("div.overflow-y-auto"), 600);
    await screen.findByText("reply 2");
    expect(stream.scrollTop).toBe(600);

    stream.scrollTop = 60;
    fireEvent.scroll(stream);
    messages = [reply(1), reply(2), reply(3)];
    sizeStream(stream, 700);
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ["messages", "s", "th-1"] });
    });

    expect(await screen.findByText("reply 3")).toBeTruthy();
    expect(stream.scrollTop).toBe(60);
    fireEvent.click(screen.getByRole("button", { name: /1 new message/ }));
    expect(stream.scrollTop).toBe(700);
  });
});

describe("TaskDetail", () => {
  it("keeps the discussion pinned to new messages", async () => {
    vi.spyOn(api, "getTask").mockResolvedValue({ task });
    let messages = [reply(1)];
    vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages, read_seq: 0, following: true }));
    const { container, qc } = renderPane({ kind: "task", taskID: task.id });
    await screen.findByRole("textbox", { name: "Reply in #14…" });
    const stream = sizeStream(container.querySelector("div.overflow-y-auto.scroll-thin"), 400);
    await screen.findByText("reply 1");
    expect(stream.scrollTop).toBe(400);

    stream.scrollTop = 40;
    fireEvent.scroll(stream);
    messages = [reply(1), reply(2)];
    sizeStream(stream, 500);
    await act(async () => {
      await qc.invalidateQueries({ queryKey: ["messages", "s", "conv-task"] });
    });

    expect(await screen.findByText("reply 2")).toBeTruthy();
    expect(stream.scrollTop).toBe(40);
    fireEvent.click(screen.getByRole("button", { name: /1 new message/ }));
    expect(stream.scrollTop).toBe(500);
  });

  it("sends on Enter and keeps a newline on Shift+Enter", async () => {
    vi.spyOn(api, "getTask").mockResolvedValue({ task });
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [reply(1)], read_seq: 0, following: true });
    renderPane({ kind: "task", taskID: task.id });
    const box = (await screen.findByRole("textbox", { name: "Reply in #14…" })) as HTMLTextAreaElement;
    act(() => box.focus());

    fireEvent.change(box, { target: { value: "first line" } });
    fireEvent.keyDown(box, { key: "Enter", shiftKey: true });
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(box);

    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith("s", "conv-task", "first line", undefined, []));
    await waitFor(() => expect(box.value).toBe(""));
    expect(document.activeElement).toBe(box);
  });

  it("edits title and description inline via amendTask", async () => {
    vi.spyOn(api, "getTask").mockResolvedValue({ task });
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [], read_seq: 0, following: true });
    const amend = vi.spyOn(api, "amendTask").mockResolvedValue({
      task: { ...task, title: "Updated title", description: "More context", revision: 2 },
    });
    renderPane({ kind: "task", taskID: task.id });

    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Task title" }), { target: { value: "Updated title" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Task description" }), { target: { value: "More context" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(amend).toHaveBeenCalledWith("s", task.id, {
        title: "Updated title",
        description: "More context",
        expected_revision: 1,
      }),
    );
  });

  it("uses custom selects for status/assignee and toggles Claim/Unclaim correctly", async () => {
    vi.spyOn(api, "getTask").mockResolvedValue({ task });
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [], read_seq: 0, following: true });
    const updateStatus = vi.spyOn(api, "updateTaskStatus").mockResolvedValue({ task: { ...task, status: "in_review" } });
    const assign = vi.spyOn(api, "assignTask").mockResolvedValue({ task: { ...task, assignee_member_id: lin.id } });
    const claim = vi.spyOn(api, "claimTask").mockResolvedValue({ task: { ...task, assignee_member_id: lisi.id } });

    const { container } = renderPane({ kind: "task", taskID: task.id });
    const statusTrigger = await screen.findByRole("button", { name: "Task status" });
    expect(container.querySelector("select")).toBeNull();

    fireEvent.click(statusTrigger);
    fireEvent.click(screen.getByRole("option", { name: /In review/ }));
    await waitFor(() => expect(updateStatus).toHaveBeenCalledWith("s", task.id, "in_review"));

    fireEvent.click(screen.getByRole("button", { name: "Task assignee" }));
    fireEvent.click(screen.getByRole("option", { name: /Lin/ }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("s", task.id, lin.id));

    fireEvent.click(screen.getByRole("button", { name: "Claim" }));
    await waitFor(() => expect(claim).toHaveBeenCalledWith("s", task.id));
  });

  it("collapses and expands task details while keeping summary visible and honoring Edit", async () => {
    const detailedTask = {
      ...task,
      description: "Detailed rollout plan for the unified watermark.",
      assignee_member_id: lin.id,
      assignee_display_name: lin.display_name,
      assignee_kind: lin.kind,
    };
    vi.spyOn(api, "getTask").mockResolvedValue({ task: detailedTask });
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [reply(1)], read_seq: 0, following: true });

    renderPane({ kind: "task", taskID: detailedTask.id });
    expect(await screen.findByText("Detailed rollout plan for the unified watermark.")).toBeTruthy();

    // Status and assignee selects share identical sizing classes for visual alignment.
    const statusWrap = screen.getByRole("button", { name: "Task status" }).parentElement!;
    const assigneeWrap = screen.getByRole("button", { name: "Task assignee" }).parentElement!;
    expect(statusWrap.className).toContain("w-[196px]");
    expect(assigneeWrap.className).toContain("w-[196px]");

    fireEvent.click(screen.getByRole("button", { name: "Collapse task details" }));
    expect(screen.queryByText("Detailed rollout plan for the unified watermark.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Task status" })).toBeNull();
    expect(screen.getByText("Unified read watermark")).toBeTruthy();
    expect(screen.getByText("In progress")).toBeTruthy();
    expect(screen.getByText("Lin")).toBeTruthy();

    // Clicking Edit while collapsed opens the inline editor immediately.
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByRole("textbox", { name: "Task title" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("button", { name: "Task status" })).toBeNull();

    // Clicking the compact summary expands the details back.
    fireEvent.click(screen.getByRole("button", { name: "Expand task details" }));
    expect(screen.getByText("Detailed rollout plan for the unified watermark.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Task status" })).toBeTruthy();
  });
});

describe("AgentDetail activity", () => {
  function renderAgent(activity: { tool?: string; stalled_seconds?: number }) {
    const member = {
      id: "a1",
      kind: "agent",
      display_name: "Lin",
      agent: { role_text: "", driver_kind: "builtin", runtime_state: "working", activity },
    };
    const data = { ...boot, members: [member] } as unknown as BootstrapData;
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <DetailPane spaceSlug="s" boot={data} target={{ kind: "agent", memberID: "a1" }} onClose={() => {}} onSelectTarget={() => {}} />
      </QueryClientProvider>,
    );
  }

  it("shows the running tool", () => {
    renderAgent({ tool: "execute" });
    expect(screen.getByText("Running execute")).toBeTruthy();
  });

  it("warns when a tool has been silent", () => {
    renderAgent({ tool: "execute", stalled_seconds: 720 });
    expect(screen.getByText("execute: no output for 12 min")).toBeTruthy();
  });

  it("switches to the Workspace tab and previews a file", async () => {
    const listSpy = vi.spyOn(api, "listAgentWorkspace").mockResolvedValue({
      root_path: "/tmp/agents/a1/workspace",
      path: "",
      entries: [{ name: "notes.md", path: "notes.md", is_dir: false, size: 12, mtime_ms: Date.now() }],
    });
    const readSpy = vi.spyOn(api, "readAgentWorkspaceFile").mockResolvedValue({
      path: "notes.md",
      size: 12,
      mtime_ms: Date.now(),
      binary: false,
      encoding: "utf8",
      content: "Hello world!",
    });

    renderAgent({});
    fireEvent.click(screen.getByRole("button", { name: "Workspace" }));
    await waitFor(() => expect(listSpy).toHaveBeenCalledWith("s", "a1", "", false));
    fireEvent.click(await screen.findByRole("button", { name: /notes\.md/ }));
    await waitFor(() => expect(readSpy).toHaveBeenCalledWith("s", "a1", "notes.md"));
    expect(await screen.findByText("Hello world!")).toBeTruthy();
  });

  it("switches to the Memory tab and renders MEMORY.md", async () => {
    const listSpy = vi.spyOn(api, "listAgentMemory").mockResolvedValue({
      root_path: "/tmp/agents/a1/memory",
      path: "",
      entries: [
        { name: "notes", path: "notes", is_dir: true, size: 0, mtime_ms: Date.now() },
        { name: "MEMORY.md", path: "MEMORY.md", is_dir: false, size: 28, mtime_ms: Date.now() },
      ],
    });
    const readSpy = vi.spyOn(api, "readAgentMemoryFile").mockResolvedValue({
      path: "MEMORY.md",
      size: 28,
      mtime_ms: Date.now(),
      binary: false,
      encoding: "utf8",
      content: "# Long-term memory\n\nOwns API.",
    });

    renderAgent({});
    fireEvent.click(screen.getByRole("button", { name: "Memory" }));
    await waitFor(() => expect(listSpy).toHaveBeenCalledWith("s", "a1", "", false));
    fireEvent.click(await screen.findByRole("button", { name: /MEMORY\.md/ }));
    await waitFor(() => expect(readSpy).toHaveBeenCalledWith("s", "a1", "MEMORY.md"));
    expect(await screen.findByText("Long-term memory")).toBeTruthy();
    expect(screen.getByText("Owns API.")).toBeTruthy();
  });

  it("shows a clear empty state when the agent has no memory files yet", async () => {
    const listSpy = vi.spyOn(api, "listAgentMemory").mockResolvedValue({
      root_path: "/tmp/agents/a1/memory",
      path: "",
      entries: [],
    });

    renderAgent({});
    fireEvent.click(screen.getByRole("button", { name: "Memory" }));
    await waitFor(() => expect(listSpy).toHaveBeenCalledWith("s", "a1", "", false));
    expect(await screen.findByText("No memory yet")).toBeTruthy();
    expect(screen.getByText("Lin doesn't have any memory files yet.")).toBeTruthy();
  });

  it("opens directly to a targeted file in the Workspace tab when jumped from an agent message", async () => {
    const listSpy = vi.spyOn(api, "listAgentWorkspace").mockResolvedValue({
      root_path: "/tmp/agents/a1/workspace",
      path: "",
      entries: [],
    });
    const readSpy = vi.spyOn(api, "readAgentWorkspaceFile").mockResolvedValue({
      path: "docs/report.md",
      size: 16,
      mtime_ms: Date.now(),
      binary: false,
      encoding: "utf8",
      content: "# Sprint Report\n",
    });

    const member: Member = {
      id: "a1",
      space_id: "s",
      kind: "agent",
      display_name: "Lin",
      access_level: "member",
      agent: { role_text: "Backend engineer", driver_kind: "acp", runtime_state: "idle" },
      created_at: "",
    };
    const data = { ...boot, members: [member] } as unknown as BootstrapData;
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <DetailPane
          spaceSlug="s"
          boot={data}
          target={{ kind: "agent", memberID: "a1", tab: "workspace", filePath: "docs/report.md", openNonce: 1 }}
          onClose={() => {}}
          onSelectTarget={() => {}}
        />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(readSpy).toHaveBeenCalledWith("s", "a1", "docs/report.md"));
    expect(listSpy).toHaveBeenCalledWith("s", "a1", "docs", false);
    expect(await screen.findByText("Sprint Report")).toBeTruthy();
  });

  it("copies an agent config and saves a template directly from AgentDetail", () => {
    const onCopyAgent = vi.fn();
    const member: Member = {
      id: "a1",
      space_id: "s",
      kind: "agent",
      display_name: "Lin",
      access_level: "member",
      agent: {
        computer_id: "c1",
        role_text: "Backend engineer",
        driver_kind: "acp",
        driver_config: { provider: "pi", pi_provider: "openai", pi_model: "gpt-5-mini" },
        runtime_state: "idle",
      },
      created_at: "",
    };
    const data = { ...boot, members: [member] } as unknown as BootstrapData;
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <DetailPane
          spaceSlug="s"
          boot={data}
          target={{ kind: "agent", memberID: "a1" }}
          onClose={() => {}}
          onSelectTarget={() => {}}
          onCopyAgent={onCopyAgent}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Agent actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Copy to new agent/ }));
    expect(onCopyAgent).toHaveBeenCalledWith(member);

    fireEvent.click(screen.getByRole("button", { name: "Agent actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: /Save as template/ }));
    const input = screen.getByRole("textbox", { name: "Template name" }) as HTMLInputElement;
    expect(input.value).toBe("Lin");
    fireEvent.change(input, { target: { value: "Pi Backend" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    const templates = listAgentTemplates();
    expect(templates).toHaveLength(1);
    expect(templates[0]).toMatchObject({
      name: "Pi Backend",
      role_text: "Backend engineer",
      computer_id: "c1",
      driver_kind: "acp",
      driver_config: { provider: "pi", pi_provider: "openai", pi_model: "gpt-5-mini" },
    });
  });

  it("applies a saved template when editing an agent driver, keeping blank fields and saving back to the agent", async () => {
    saveAgentTemplate({
      name: "Pi Anthropic",
      role_text: "Unused during driver edit",
      driver_kind: "acp",
      driver_config: { provider: "pi", pi_provider: "anthropic", pi_model: "claude-sonnet" },
    });
    const member: Member = {
      id: "a1",
      space_id: "s",
      kind: "agent",
      display_name: "Lin",
      access_level: "member",
      agent: {
        computer_id: "c1",
        role_text: "Backend engineer",
        driver_kind: "acp",
        driver_config: { provider: "pi", pi_provider: "openai", pi_model: "gpt-5-mini", context_window: 272000 },
        runtime_state: "idle",
      },
      created_at: "",
    };
    const updateDriver = vi.spyOn(api, "updateAgentDriver").mockResolvedValue({ agent: member });
    const data = {
      ...boot,
      members: [member],
      computers: [
        { id: "c1", name: "mac", hostname: "mac.local", os: "macos", connection_status: "online", acp_providers: ["pi", "claude"], created_at: "" },
      ],
    } as unknown as BootstrapData;
    renderPane({ kind: "agent", memberID: "a1" }, data);

    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const group = screen.getByRole("group", { name: "Driver templates" });
    fireEvent.click(within(group).getByRole("button", { name: /Pi Anthropic/ }));

    expect((screen.getByLabelText(/pi provider/) as HTMLInputElement).value).toBe("anthropic");
    expect((screen.getByLabelText(/pi model/) as HTMLInputElement).value).toBe("claude-sonnet");
    // Blank context_window in the template preserves the agent's existing value.
    expect((screen.getByLabelText(/Context window/) as HTMLInputElement).value).toBe("272000");

    fireEvent.click(screen.getByRole("button", { name: "Save and restart" }));
    await waitFor(() =>
      expect(updateDriver).toHaveBeenCalledWith("s", "a1", "acp", {
        provider: "pi",
        pi_provider: "anthropic",
        pi_model: "claude-sonnet",
        context_window: 272000,
      }),
    );
  });

  it("never copies API keys from templates in edit mode, preserves typed keys, and supports reset", async () => {
    saveAgentTemplate({
      name: "New Gateway",
      role_text: "",
      driver_kind: "builtin",
      driver_config: { base_url: "http://127.0.0.1:4000/v1", model: "gpt-5" },
    });
    const member: Member = {
      id: "a2",
      space_id: "s",
      kind: "agent",
      display_name: "Kira",
      access_level: "member",
      agent: {
        computer_id: "c1",
        role_text: "Reviewer",
        driver_kind: "builtin",
        driver_config: { base_url: "http://127.0.0.1:3000/v1", model: "old-model", api_key_set: true },
        runtime_state: "idle",
      },
      created_at: "",
    };
    const updateDriver = vi.spyOn(api, "updateAgentDriver").mockResolvedValue({ agent: member });
    const data = { ...boot, members: [member] } as unknown as BootstrapData;
    renderPane({ kind: "agent", memberID: "a2" }, data);

    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const group = screen.getByRole("group", { name: "Driver templates" });
    fireEvent.click(within(group).getByRole("button", { name: /New Gateway/ }));

    expect((screen.getByLabelText("Base URL") as HTMLInputElement).value).toBe("http://127.0.0.1:4000/v1");
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("gpt-5");
    expect((screen.getByLabelText(/^API key/) as HTMLInputElement).value).toBe("");

    // Reset restores the agent's original driver fields.
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect((screen.getByLabelText("Base URL") as HTMLInputElement).value).toBe("http://127.0.0.1:3000/v1");
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("old-model");

    // Re-applying after typing an API key keeps the typed key intact; omitting it leaves server key unchanged.
    fireEvent.click(within(group).getByRole("button", { name: /New Gateway/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save and restart" }));
    await waitFor(() =>
      expect(updateDriver).toHaveBeenCalledWith("s", "a2", "builtin", {
        base_url: "http://127.0.0.1:4000/v1",
        model: "gpt-5",
      }),
    );

    // Reopen and verify an explicitly typed API key is preserved across template application.
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText(/^API key/), { target: { value: "sk-typed-by-user" } });
    fireEvent.click(within(screen.getByRole("group", { name: "Driver templates" })).getByRole("button", { name: /New Gateway/ }));
    expect((screen.getByLabelText(/^API key/) as HTMLInputElement).value).toBe("sk-typed-by-user");
  });

  it("exposes Stop button for working agents to owners/admins and sends stop_turn", async () => {
    const ctrl = vi.spyOn(api, "controlAgent").mockResolvedValue({ ok: true });
    const workingLin: Member = {
      ...lin,
      agent: { ...lin.agent!, runtime_state: "working", focus_conversation_id: "c-eng" },
    };
    const data = {
      ...roster,
      members: [lisi, workingLin, sam],
      conversations: [{ id: "c-eng", space_id: "s", kind: "public", slug: "eng", unread_count: 0, notify_level: "mentions" }],
    } as unknown as BootstrapData;

    renderPane({ kind: "agent", memberID: lin.id }, data);
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(ctrl).toHaveBeenCalledWith("s", lin.id, "stop_turn"));
  });

  it("hides Stop button when agent is idle or viewer is a regular member", () => {
    renderPane({ kind: "agent", memberID: lin.id }, roster);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();

    const workingLin: Member = {
      ...lin,
      agent: { ...lin.agent!, runtime_state: "working", focus_conversation_id: "c-eng" },
    };
    const memberView = { ...roster, members: [lisi, workingLin, sam], current_member: sam } as unknown as BootstrapData;
    renderPane({ kind: "agent", memberID: lin.id }, memberView);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });
});

describe("HumanDetail and thread @mentions", () => {
  it("shows human profile, computers, tasks, and starts a DM", () => {
    const onSelectTarget = vi.fn();
    const onOpenDM = vi.fn();
    const data = {
      ...roster,
      computers: [
        { id: "c1", owner_member_id: sam.id, name: "Sam MacBook", hostname: "sam.local", os: "macos", connection_status: "online", created_at: "2026-11-19T10:00:00Z" },
      ],
      tasks: [{ ...task, assignee_member_id: sam.id }],
    } as unknown as BootstrapData;
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <DetailPane spaceSlug="s" boot={data} target={{ kind: "human", memberID: sam.id }} onClose={() => {}} onSelectTarget={onSelectTarget} onOpenDM={onOpenDM} />
      </QueryClientProvider>,
    );

    expect(screen.getByText("Person")).toBeTruthy();
    expect(screen.getByText("@Sam")).toBeTruthy();
    expect(screen.getByText("Sam MacBook")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Message" }));
    expect(onOpenDM).toHaveBeenCalledWith(sam.id);

    fireEvent.click(screen.getByRole("button", { name: /Unified read watermark/ }));
    expect(onSelectTarget).toHaveBeenCalledWith({ kind: "task", taskID: task.id });
  });

  it("opens mentioned member detail when clicking @mention in a thread reply", async () => {
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [reply(1, lisi, "ask @Lin and @Sam")], read_seq: 0, following: true });
    const onSelectTarget = vi.fn();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <DetailPane
          spaceSlug="s"
          boot={roster}
          target={{ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" }}
          onClose={() => {}}
          onSelectTarget={onSelectTarget}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "@Lin" }));
    expect(onSelectTarget).toHaveBeenCalledWith({ kind: "agent", memberID: lin.id });

    fireEvent.click(screen.getByRole("button", { name: "@Sam" }));
    expect(onSelectTarget).toHaveBeenCalledWith({ kind: "human", memberID: sam.id });
  });
});
