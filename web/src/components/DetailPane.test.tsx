import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
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

afterEach(() => vi.restoreAllMocks());

describe("ThreadDetail", () => {
  it("starts from the server following state and toggles it", async () => {
    let following = false;
    vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages: [], read_seq: 0, following }));
    const setFollowing = vi.spyOn(api, "setFollowing").mockImplementation(async (_s, _c, next) => {
      following = next;
      return { ok: true };
    });
    renderPane({ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" });
    fireEvent.click(await screen.findByRole("button", { name: "Follow" }));
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
