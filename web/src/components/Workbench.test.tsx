import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData } from "../api";
import { Workbench } from "./Workbench";

const sampleBoot: BootstrapData = {
  space: { id: "sp-1", slug: "studio", name: "Sumi Studio", accent: "flame", owner_member_id: "m-owner", event_seq: 1, created_at: "2026-11-19T10:00:00Z" },
  current_member: { id: "m-owner", space_id: "sp-1", kind: "human", display_name: "Lisi", access_level: "owner", created_at: "2026-11-19T10:00:00Z" },
  members: [
    { id: "m-owner", space_id: "sp-1", kind: "human", display_name: "Lisi", access_level: "owner", created_at: "2026-11-19T10:00:00Z" },
    {
      id: "m-lin",
      space_id: "sp-1",
      kind: "agent",
      display_name: "Lin",
      access_level: "member",
      permissions: ["agent.create"],
      agent: {
        computer_id: "c-1",
        role_text: "Backend engineer",
        driver_kind: "acp",
        driver_config: { provider: "pi" },
        focus_conversation_id: "conv-eng",
        runtime_state: "working",
      },
      created_at: "2026-11-19T10:00:00Z",
    },
    {
      id: "m-kira",
      space_id: "sp-1",
      kind: "agent",
      display_name: "Kira",
      access_level: "member",
      agent: {
        computer_id: "c-1",
        role_text: "Code reviewer focusing on API ergonomics and edge cases",
        driver_kind: "acp",
        driver_config: { provider: "pi" },
        runtime_state: "idle",
      },
      created_at: "2026-11-19T10:00:00Z",
    },
  ],
  conversations: [
    {
      id: "conv-eng",
      space_id: "sp-1",
      kind: "public",
      slug: "engineering",
      topic: "Core runtime",
      next_seq: 2,
      read_seq: 1,
      unread_count: 0,
      mention_count: 0,
      notify_level: "mentions",
      following: false,
    },
  ],
  inbox: [
    {
      conversation_id: "conv-eng",
      kind: "public",
      title: "#engineering",
      unread_count: 1,
      mention_count: 1,
      needs_attention: true,
      reason: "mention",
      latest_seq: 2,
      latest_author_name: "Lin",
      latest_preview: "@Lisi ready for review",
      latest_at: "2026-11-19T10:20:00Z",
      following: false,
      notify_level: "mentions",
    },
  ],
  tasks: [
    {
      id: "t-14",
      space_id: "sp-1",
      number: 14,
      channel_id: "conv-eng",
      channel_slug: "engineering",
      title: "Unified read watermark",
      description: "Single conversation_states table",
      status: "in_progress",
      creator_member_id: "m-owner",
      assignee_member_id: "m-lin",
      assignee_display_name: "Lin",
      assignee_kind: "agent",
      revision: 2,
      message_count: 1,
      created_at: "2026-11-19T10:12:00Z",
      updated_at: "2026-11-19T10:15:00Z",
    },
  ],
  computers: [
    { id: "c-1", owner_member_id: "m-owner", name: "mac-studio", hostname: "mac.local", os: "macos", connection_status: "online", created_at: "2026-11-19T10:00:00Z" },
  ],
};

class MockEventSource {
  static latest: MockEventSource | null = null;
  listeners = new Map<string, Set<(e: Event) => void>>();
  constructor() {
    MockEventSource.latest = this;
  }
  addEventListener(type: string, cb: (e: Event) => void) {
    let set = this.listeners.get(type);
    if (!set) {
      set = new Set();
      this.listeners.set(type, set);
    }
    set.add(cb);
  }
  emit(type: string, data: unknown) {
    const ev = new MessageEvent(type, { data: JSON.stringify(data) });
    for (const cb of this.listeners.get(type) ?? []) cb(ev);
  }
  close() {}
}

describe("Workbench", () => {
  beforeEach(() => {
    vi.stubGlobal("EventSource", MockEventSource);
    vi.spyOn(api, "bootstrap").mockResolvedValue(sampleBoot);
    vi.spyOn(api, "listInbox").mockResolvedValue({ items: sampleBoot.inbox });
    vi.spyOn(api, "listTasks").mockResolvedValue({ tasks: sampleBoot.tasks });
    vi.spyOn(api, "getTask").mockResolvedValue({ task: sampleBoot.tasks[0] });
    vi.spyOn(api, "markDone").mockResolvedValue({ ok: true });
    vi.spyOn(api, "listMessages").mockResolvedValue({
      read_seq: 1,
      following: false,
      messages: [
        {
          id: "msg-1",
          space_id: "sp-1",
          conversation_id: "conv-eng",
          seq: 1,
          author_member_id: "m-owner",
          author_display_name: "Lisi",
          author_kind: "human",
          kind: "text",
          body_markdown: "Split into tasks",
          attached_tasks: sampleBoot.tasks,
          created_at: "2026-11-19T10:12:00Z",
        },
      ],
    });
  });

  afterEach(() => {
    sessionStorage.clear();
    vi.restoreAllMocks();
  });

  it("triages Inbox Done and opens dedicated Task DetailPane from channel", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    // Inbox renders the mention row and Done invokes markDone.
    const doneBtn = await screen.findByRole("button", { name: "Done" }, { timeout: 10000 });
    fireEvent.click(doneBtn);
    await waitFor(() => expect(api.markDone).toHaveBeenCalledWith("studio", "conv-eng"));

    // The sidebar shows which conversation a working agent is focused on (replacing role),
    // and shows the role on the second line for an idle agent with fade truncation.
    const sidebar = screen.getByRole("complementary", { name: "Sidebar" });
    expect(within(sidebar).getByText("Working in #engineering")).toBeTruthy();
    expect(within(sidebar).queryByText("Backend engineer")).toBeNull();
    const roleEl = within(sidebar).getByText("Code reviewer focusing on API ergonomics and edge cases");
    expect(roleEl.className).toContain("fade-truncate");

    // Navigate to #engineering and click attached task #14 to open Right Detail Pane.
    fireEvent.click(within(sidebar).getByRole("button", { name: /^engineering$/i }));
    const taskRow = await screen.findByRole("button", { name: /Unified read watermark/i });
    fireEvent.click(taskRow);

    await waitFor(() => expect(api.getTask).toHaveBeenCalledWith("studio", "t-14"), { timeout: 10000 });
    expect(await screen.findByText("Discussion", {}, { timeout: 10000 })).toBeTruthy();
  }, 30000);

  it("renders a unified draggable 52px titlebar header for macOS traffic lights in desktop shell", async () => {
    sessionStorage.setItem("sumi.shell", "desktop");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    await screen.findByText("Sumi Studio");
    const titlebar = container.querySelector("[data-desktop-titlebar]") as HTMLElement | null;
    expect(titlebar).toBeTruthy();
    expect(titlebar?.className).toContain("h-[52px]");
    expect(titlebar?.className).toContain("titlebar-drag");
    expect(within(titlebar!).getByText("Sumi Studio")).toBeTruthy();
  });

  it("patches agent.state_changed from SSE without refetching bootstrap", async () => {
    const bootSpy = vi.spyOn(api, "bootstrap");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    const sidebar = await screen.findByRole("complementary", { name: "Sidebar" });
    expect(within(sidebar).getByText("Working in #engineering")).toBeTruthy();
    const callsBefore = bootSpy.mock.calls.length;

    MockEventSource.latest?.emit("agent.state_changed", {
      member_id: "m-lin",
      runtime_state: "idle",
      focus_conversation_id: "",
    });

    await waitFor(() => expect(within(sidebar).queryByText("Working in #engineering")).toBeNull());
    expect(within(sidebar).getByText("Backend engineer")).toBeTruthy();
    expect(bootSpy.mock.calls.length).toBe(callsBefore);
  });

  it("toggles sidebar with Cmd+B, opens shortcuts with Cmd+/, and searches messages in Cmd+K", async () => {
    const searchSpy = vi.spyOn(api, "searchMessages").mockResolvedValue({
      hits: [
        {
          message_id: "msg-1",
          conversation_id: "conv-eng",
          conversation_kind: "public",
          conversation_title: "#engineering",
          seq: 1,
          author_member_id: "m-owner",
          author_display_name: "Lisi",
          author_kind: "human",
          snippet: "Split into tasks",
          created_at: "2026-11-19T10:12:00Z",
        },
      ],
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    expect(await screen.findByRole("complementary", { name: "Sidebar" })).toBeTruthy();

    fireEvent.keyDown(window, { key: "b", metaKey: true });
    expect(screen.queryByRole("complementary", { name: "Sidebar" })).toBeNull();
    fireEvent.keyDown(window, { key: "b", metaKey: true });
    expect(screen.getByRole("complementary", { name: "Sidebar" })).toBeTruthy();

    fireEvent.keyDown(window, { key: "/", metaKey: true });
    expect(await screen.findByRole("dialog", { name: "Keyboard shortcuts" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });

    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const palette = await screen.findByRole("dialog", { name: "Command palette" });
    fireEvent.change(within(palette).getByPlaceholderText(/Search channels/i), { target: { value: "Split" } });
    await waitFor(() => expect(searchSpy).toHaveBeenCalledWith("studio", "Split", { limit: 8 }));
    expect(await within(palette).findByText("Messages")).toBeTruthy();
  });
});
