import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData } from "../api";
import { applySpellCheck, clearPreferencesForTests, readFontFamily, readFontSize, readLongMessagesPref, readSpellCheck } from "../preferences";
import { AdminPanel, InboxView, TasksView, type AdminTab } from "./Views";

const sampleBoot: BootstrapData = {
  space: {
    id: "sp-1",
    slug: "studio",
    name: "Sumi Studio",
    accent: "flame",
    owner_member_id: "m-lisi",
    event_seq: 1,
    created_at: "2026-11-19T10:00:00Z",
  },
  current_member: {
    id: "m-lisi",
    space_id: "sp-1",
    kind: "human",
    display_name: "Lisi",
    access_level: "owner",
    created_at: "2026-11-19T10:00:00Z",
  },
  members: [
    {
      id: "m-lisi",
      space_id: "sp-1",
      kind: "human",
      display_name: "Lisi",
      access_level: "owner",
      created_at: "2026-11-19T10:00:00Z",
    },
    {
      id: "m-sam",
      space_id: "sp-1",
      kind: "human",
      display_name: "Sam",
      access_level: "member",
      created_at: "2026-11-19T10:00:00Z",
    },
    {
      id: "m-lin",
      space_id: "sp-1",
      kind: "agent",
      display_name: "Lin",
      access_level: "member",
      agent: {
        computer_id: "c-1",
        role_text: "Backend engineer",
        driver_kind: "acp",
        driver_config: { provider: "pi", pi_provider: "openai", pi_model: "gpt-5-mini" },
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
        role_text: "Code reviewer",
        driver_kind: "builtin",
        driver_config: { base_url: "http://127.0.0.1:4000/v1", model: "claude-sonnet" },
        runtime_state: "idle",
      },
      created_at: "2026-11-19T10:00:00Z",
    },
    {
      id: "m-ops",
      space_id: "sp-1",
      kind: "agent",
      display_name: "OpsBot",
      access_level: "member",
      agent: {
        computer_id: "c-2",
        role_text: "Release monitor",
        driver_kind: "acp",
        driver_config: { provider: "claude" },
        runtime_state: "error",
        last_error_code: "driver_config_invalid",
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
      next_seq: 1,
      read_seq: 1,
      unread_count: 0,
      mention_count: 0,
      notify_level: "mentions",
      following: false,
    },
  ],
  inbox: [],
  tasks: [],
  computers: [
    {
      id: "c-1",
      owner_member_id: "m-lisi",
      name: "mac-studio",
      hostname: "mac.local",
      os: "macos",
      connection_status: "online",
      acp_providers: ["pi", "claude"],
      created_at: "2026-11-19T10:00:00Z",
    },
    {
      id: "c-2",
      owner_member_id: "m-sam",
      name: "linux-box",
      hostname: "box.internal",
      os: "linux",
      connection_status: "offline",
      acp_providers: ["codex"],
      created_at: "2026-11-19T10:00:00Z",
    },
  ],
};

function renderAdmin(tab: AdminTab, overrides: Partial<Parameters<typeof AdminPanel>[0]> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props: Parameters<typeof AdminPanel>[0] = {
    boot: sampleBoot,
    tab,
    onTab: vi.fn(),
    onInspectAgent: vi.fn(),
    onInspectHuman: vi.fn(),
    onOpenDM: vi.fn(),
    onNewAgent: vi.fn(),
    onCopyAgent: vi.fn(),
    onConnectComputer: vi.fn(),
    theme: "system",
    onTheme: vi.fn(),
    onLogout: vi.fn(),
    ...overrides,
  };
  const view = render(
    <QueryClientProvider client={qc}>
      <AdminPanel {...props} />
    </QueryClientProvider>,
  );
  return { ...view, props };
}

afterEach(() => {
  vi.restoreAllMocks();
  clearPreferencesForTests();
  delete document.documentElement.dataset.font;
  delete document.documentElement.dataset.fontSize;
  applySpellCheck("on");
});

describe("AdminPanel", () => {
  it("filters agents by status and search query and triggers quick DM and copy actions", () => {
    const { props } = renderAdmin("agents");

    // Status filter tabs include Error when an agent is in error state.
    const statusTabs = screen.getByRole("tablist", { name: "Filter agents by status" });
    expect(within(statusTabs).getByRole("tab", { name: /All 3/ })).toBeTruthy();
    expect(within(statusTabs).getByRole("tab", { name: /Working 1/ })).toBeTruthy();
    expect(within(statusTabs).getByRole("tab", { name: /Idle 1/ })).toBeTruthy();
    expect(within(statusTabs).getByRole("tab", { name: /Error 1/ })).toBeTruthy();

    fireEvent.click(within(statusTabs).getByRole("tab", { name: /Error 1/ }));
    expect(screen.getByText("OpsBot")).toBeTruthy();
    expect(screen.queryByText("Lin")).toBeNull();
    expect(screen.queryByText("Kira")).toBeNull();

    fireEvent.click(within(statusTabs).getByRole("tab", { name: /All 3/ }));
    fireEvent.change(screen.getByRole("textbox", { name: "Search agents" }), { target: { value: "gpt-5-mini" } });
    expect(screen.getByText("Lin")).toBeTruthy();
    expect(screen.queryByText("Kira")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Message Lin" }));
    expect(props.onOpenDM).toHaveBeenCalledWith("m-lin");

    fireEvent.click(screen.getByRole("button", { name: "Copy Lin to new agent" }));
    expect(props.onCopyAgent).toHaveBeenCalledWith(expect.objectContaining({ id: "m-lin" }));
  });

  it("renders Settings General section with theme, typography, long messages, space accent, and sign out", async () => {
    vi.spyOn(api, "listArchivedChannels").mockResolvedValue({ conversations: [] });
    const updateSpace = vi.spyOn(api, "updateSpace").mockResolvedValue({
      space: { ...sampleBoot.space, accent: "sky" },
    });
    const { props } = renderAdmin("settings");

    expect(screen.getByRole("heading", { name: "General" })).toBeTruthy();

    const themeGroup = screen.getByRole("radiogroup", { name: "Theme" });
    fireEvent.click(within(themeGroup).getByRole("radio", { name: "Dark" }));
    expect(props.onTheme).toHaveBeenCalledWith("dark");
    fireEvent.click(within(themeGroup).getByRole("radio", { name: "System" }));
    expect(props.onTheme).toHaveBeenCalledWith("system");

    const fontGroup = screen.getByRole("tablist", { name: "Font family" });
    fireEvent.click(within(fontGroup).getByRole("tab", { name: "Serif" }));
    expect(document.documentElement.dataset.font).toBe("serif");
    expect(readFontFamily()).toBe("serif");

    const sizeGroup = screen.getByRole("tablist", { name: "Text size" });
    fireEvent.click(within(sizeGroup).getByRole("tab", { name: "Large" }));
    expect(document.documentElement.dataset.fontSize).toBe("lg");
    expect(readFontSize()).toBe("lg");

    const longMsgGroup = screen.getByRole("tablist", { name: "Long messages" });
    expect(within(longMsgGroup).getByRole("tab", { name: "Expanded" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(within(longMsgGroup).getByRole("tab", { name: "Collapsed" }));
    expect(readLongMessagesPref()).toBe("collapsed");

    const spellGroup = screen.getByRole("tablist", { name: "Spell check" });
    expect(within(spellGroup).getByRole("tab", { name: "On" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(within(spellGroup).getByRole("tab", { name: "Off" }));
    expect(readSpellCheck()).toBe("off");
    expect(document.documentElement.dataset.spellcheck).toBe("off");
    fireEvent.click(within(spellGroup).getByRole("tab", { name: "On" }));
    expect(readSpellCheck()).toBe("on");
    expect(document.documentElement.dataset.spellcheck).toBeUndefined();

    const accentGroup = screen.getByRole("radiogroup", { name: "Space accent" });
    fireEvent.click(within(accentGroup).getByRole("radio", { name: "Sky" }));
    await waitFor(() => expect(updateSpace).toHaveBeenCalledWith("studio", { accent: "sky" }));

    expect(await screen.findByText("No archived channels in this space.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    expect(props.onLogout).toHaveBeenCalledTimes(1);
  });
});

describe("InboxView and TasksView polish", () => {
  it("supports Mark all done in InboxView when multiple items need attention", async () => {
    vi.spyOn(api, "listInbox").mockResolvedValue({
      items: [
        {
          conversation_id: "conv-1",
          kind: "public",
          title: "#eng",
          unread_count: 1,
          mention_count: 1,
          needs_attention: true,
          reason: "mention",
          latest_seq: 2,
          latest_author_name: "Lin",
          latest_preview: "@Lisi check this",
          latest_at: "2026-11-19T10:20:00Z",
          following: false,
          notify_level: "mentions",
        },
        {
          conversation_id: "conv-2",
          kind: "dm",
          title: "Kira",
          unread_count: 1,
          mention_count: 0,
          needs_attention: true,
          reason: "dm",
          latest_seq: 3,
          latest_author_name: "Kira",
          latest_preview: "PR ready",
          latest_at: "2026-11-19T10:22:00Z",
          following: false,
          notify_level: "all",
        },
      ],
    });
    const doneSpy = vi.spyOn(api, "markDone").mockResolvedValue({ ok: true });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <InboxView spaceSlug="studio" filter="needs_me" onFilter={vi.fn()} onOpenConversation={vi.fn()} onOpenDetail={vi.fn()} />
      </QueryClientProvider>,
    );

    const markAll = await screen.findByRole("button", { name: "Mark all done" });
    fireEvent.click(markAll);
    await waitFor(() => {
      expect(doneSpy).toHaveBeenCalledWith("studio", "conv-1");
      expect(doneSpy).toHaveBeenCalledWith("studio", "conv-2");
    });
  });

  it("clears task search filter and resets empty-filter state in TasksView", async () => {
    const taskBoot: BootstrapData = {
      ...sampleBoot,
      tasks: [
        {
          id: "t-1",
          space_id: "sp-1",
          number: 1,
          channel_id: "conv-eng",
          channel_slug: "engineering",
          title: "Fix retry loop",
          description: "",
          status: "todo",
          creator_member_id: "m-lisi",
          revision: 1,
          message_count: 0,
          created_at: "2026-11-19T10:00:00Z",
          updated_at: "2026-11-19T10:00:00Z",
        },
      ],
    };
    vi.spyOn(api, "listTasks").mockResolvedValue({ tasks: taskBoot.tasks });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <TasksView spaceSlug="studio" boot={taskBoot} filter="active" onFilter={vi.fn()} onOpenDetail={vi.fn()} />
      </QueryClientProvider>,
    );

    expect(await screen.findByText("Fix retry loop")).toBeTruthy();
    const input = screen.getByRole("textbox", { name: "Filter tasks" });
    fireEvent.change(input, { target: { value: "nonexistent" } });
    expect(screen.getByText("No tasks match the current filter")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Reset filters" }));
    expect(screen.getByText("Fix retry loop")).toBeTruthy();
  });
});
