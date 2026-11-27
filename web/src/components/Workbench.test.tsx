import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData } from "../api";
import { App } from "../App";
import { clearAllDraftsForTests, writeDraft } from "../drafts";
import { clearLastLocation, writeLastSpaceView } from "../preferences";
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
    clearAllDraftsForTests();
    clearLastLocation();
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
    expect(within(sidebar).getByText("Lisi")).toBeTruthy();
    expect(within(sidebar).queryByText(/^owner$/i)).toBeNull();

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
    // Collapsed rail keeps quick navigation for Jump, Inbox, and Tasks.
    const rail = screen.getByRole("navigation", { name: "Spaces" });
    expect(within(rail).getByRole("button", { name: "Jump to…" })).toBeTruthy();
    expect(within(rail).getByRole("button", { name: "Inbox" })).toBeTruthy();
    expect(within(rail).getByRole("button", { name: "Tasks" })).toBeTruthy();
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

  it("opens the same New agent form prefilled when copying an existing agent", async () => {
    const createSpy = vi.spyOn(api, "createAgent").mockResolvedValue({
      agent: {
        id: "m-lin-2",
        space_id: "sp-1",
        kind: "agent",
        display_name: "Lin_2",
        access_level: "member",
        agent: { computer_id: "c-1", role_text: "Backend engineer", driver_kind: "acp", driver_config: { provider: "pi" }, runtime_state: "idle" },
        created_at: "2026-11-19T10:30:00Z",
      },
    });
    vi.spyOn(api, "createDM").mockResolvedValue({
      conversation: {
        id: "dm-lin-2",
        space_id: "sp-1",
        kind: "dm",
        peer_member_id: "m-lin-2",
        peer_display_name: "Lin_2",
        peer_kind: "agent",
        next_seq: 1,
        read_seq: 0,
        unread_count: 0,
        mention_count: 0,
        following: false,
      },
    });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    fireEvent.click(await screen.findByRole("button", { name: "Agents" }));
    fireEvent.click(await screen.findByRole("button", { name: "Copy Lin to new agent" }));

    const dialog = await screen.findByRole("dialog", { name: "New agent" });
    expect((within(dialog).getByLabelText("Name") as HTMLInputElement).value).toBe("Lin_2");
    expect((within(dialog).getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Backend engineer");

    fireEvent.click(within(dialog).getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(createSpy).toHaveBeenCalledWith("studio", {
        display_name: "Lin_2",
        role_text: "Backend engineer",
        computer_id: "c-1",
        driver_kind: "acp",
        driver_config: { provider: "pi" },
      }),
    );
  });

  it("remembers the last visited space and channel across restarts and falls back if removed", async () => {
    const secondSpace = {
      id: "sp-2",
      slug: "ops",
      name: "Ops Space",
      accent: "flame" as const,
      owner_member_id: "m-owner",
      event_seq: 1,
      created_at: "2026-11-19T10:00:00Z",
    };
    const opsBoot: BootstrapData = {
      ...sampleBoot,
      space: secondSpace,
      conversations: [
        {
          id: "conv-ops-alerts",
          space_id: "sp-2",
          kind: "public",
          slug: "alerts",
          topic: "Production alerts",
          next_seq: 1,
          read_seq: 1,
          unread_count: 0,
          mention_count: 0,
          notify_level: "mentions",
          following: false,
        },
      ],
    };
    vi.spyOn(api, "me").mockResolvedValue({
      user: { id: "u-1", email: "lisi@example.com", display_name: "Lisi", created_at: "2026-11-19T10:00:00Z" },
      spaces: [sampleBoot.space, secondSpace],
    });
    const bootSpy = vi.spyOn(api, "bootstrap").mockImplementation(async (slug) => (slug === "ops" ? opsBoot : sampleBoot));
    vi.spyOn(api, "listMessages").mockResolvedValue({ read_seq: 1, following: false, messages: [] });

    // First session: switch to the second space and open #alerts.
    const qc1 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const first = render(
      <QueryClientProvider client={qc1}>
        <App />
      </QueryClientProvider>,
    );
    await screen.findByText("Sumi Studio");
    fireEvent.click(screen.getByRole("button", { name: "Space Ops Space" }));
    const opsSidebar = await screen.findByRole("complementary", { name: "Sidebar" });
    fireEvent.click(await within(opsSidebar).findByRole("button", { name: /^alerts$/i }));
    await screen.findByText("Production alerts");
    first.unmount();

    // Restart: App lands directly in Ops Space -> #alerts (even though inbox is non-empty).
    bootSpy.mockClear();
    const qc2 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const second = render(
      <QueryClientProvider client={qc2}>
        <App />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("Production alerts")).toBeTruthy();
    expect(bootSpy).toHaveBeenCalledWith("ops");
    second.unmount();

    // Stale saved channel falls back cleanly to Inbox when inbox is non-empty.
    writeLastSpaceView("studio", { kind: "conversation", convID: "conv-deleted" });
    const qc3 = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc3}>
        <App />
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("button", { name: "Done" })).toBeTruthy();
  });

  it("exposes working-agents popover, agent context menu, DM draft indicator, unread jump shortcut, and Unread palette group", async () => {
    const dmLin = {
      id: "dm-lin",
      space_id: "sp-1",
      kind: "dm" as const,
      peer_member_id: "m-lin",
      peer_display_name: "Lin",
      peer_kind: "agent" as const,
      next_seq: 3,
      read_seq: 1,
      unread_count: 2,
      mention_count: 0,
      following: false,
    };
    const unreadBoot: BootstrapData = {
      ...sampleBoot,
      conversations: [
        { ...sampleBoot.conversations[0], unread_count: 1, mention_count: 1 },
        dmLin,
      ],
    };
    vi.spyOn(api, "bootstrap").mockResolvedValue(unreadBoot);
    const ctrlSpy = vi.spyOn(api, "controlAgent").mockResolvedValue({ ok: true });
    writeDraft("dm-lin", { text: "follow up later", replyTo: null });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <Workbench spaceSlug="studio" onLogout={() => {}} />
      </QueryClientProvider>,
    );

    const sidebar = await screen.findByRole("complementary", { name: "Sidebar" });

    // DM row shows unsent draft pencil indicator.
    expect(within(sidebar).getByLabelText("Has draft")).toBeTruthy();

    // Working-agents badge opens the shared popover.
    fireEvent.click(within(sidebar).getByRole("button", { name: "1 working" }));
    const pop = await screen.findByRole("dialog", { name: "Working now" });
    expect(within(pop).getByText("Lin")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });

    // Agent right-click context menu allows quick stop/inspect actions.
    fireEvent.contextMenu(within(sidebar).getByRole("button", { name: /Lin/ }));
    const menu = await screen.findByRole("menu", { name: "Lin menu" });
    expect(within(menu).getByRole("menuitem", { name: "Open workspace" })).toBeTruthy();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Stop turn" }));
    await waitFor(() => expect(ctrlSpy).toHaveBeenCalledWith("studio", "m-lin", "stop_turn"));

    // Cmd+K surfaces the Unread group at the top.
    fireEvent.keyDown(window, { key: "k", metaKey: true });
    const palette = await screen.findByRole("dialog", { name: "Command palette" });
    expect(within(palette).getByText("Unread")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });

    // Alt+Shift+ArrowDown jumps directly to the first unread conversation (#engineering).
    fireEvent.keyDown(window, { key: "ArrowDown", altKey: true, shiftKey: true });
    expect(await screen.findByRole("heading", { name: "engineering" })).toBeTruthy();

    // Pressing Alt+Shift+ArrowDown again cycles to the next unread conversation (DM with Lin).
    fireEvent.keyDown(window, { key: "ArrowDown", altKey: true, shiftKey: true });
    expect(await screen.findByRole("heading", { name: "Lin" })).toBeTruthy();
  });
});
