import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData, type Conversation, type Member, type Message } from "../api";
import { REACTION_EMOJIS } from "../emojis";
import { ConversationView } from "./ConversationView";
import { DetailPane } from "./DetailPane";

const lisi: Member = { id: "m-lisi", space_id: "s", kind: "human", display_name: "Lisi", access_level: "owner", created_at: "2026-11-19T10:00:00Z" };
const lin: Member = {
  id: "m-lin",
  space_id: "s",
  kind: "agent",
  display_name: "Lin",
  access_level: "member",
  agent: { role_text: "Backend engineer", driver_kind: "acp", runtime_state: "idle" },
  created_at: "2026-11-19T10:00:00Z",
};

const conv: Conversation = {
  id: "conv-eng",
  space_id: "s",
  kind: "public",
  slug: "engineering",
  next_seq: 2,
  read_seq: 0,
  unread_count: 0,
  mention_count: 0,
  notify_level: "mentions",
  following: false,
};

const boot: BootstrapData = {
  space: { id: "s", slug: "studio", name: "Studio", accent: "flame", owner_member_id: lisi.id, event_seq: 1, created_at: "2026-11-19T10:00:00Z" },
  current_member: lisi,
  members: [lisi, lin],
  conversations: [conv],
  inbox: [],
  tasks: [],
  computers: [],
};

function makeMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    space_id: "s",
    conversation_id: conv.id,
    seq: 1,
    author_member_id: lin.id,
    author_display_name: lin.display_name,
    author_kind: lin.kind,
    kind: "text",
    body_markdown: "Deploy complete",
    created_at: "2026-11-19T10:01:00Z",
    ...overrides,
  };
}

afterEach(() => vi.restoreAllMocks());

describe("message reactions", () => {
  it("opens the curated 8-emoji picker and toggles add/remove in ConversationView", async () => {
    vi.spyOn(api, "listMessages").mockResolvedValue({ messages: [makeMessage()], read_seq: 0, following: false });
    vi.spyOn(api, "listConversationMembers").mockResolvedValue({ members: [lisi, lin] });
    const addSpy = vi.spyOn(api, "addReaction").mockResolvedValue({
      reactions: [{ emoji: "rocket", count: 1, member_ids: [lisi.id], reacted: true }],
    });
    const removeSpy = vi.spyOn(api, "removeReaction").mockResolvedValue({ reactions: [] });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <ConversationView spaceSlug="studio" boot={boot} conv={conv} onOpenDetail={() => {}} />
      </QueryClientProvider>,
    );

    await screen.findByText("Deploy complete");
    const row = screen.getByText("Deploy complete").closest("article")!;
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "Add reaction" }));

    const dialog = screen.getByRole("dialog", { name: "Pick a reaction" });
    expect(within(dialog).getAllByRole("button")).toHaveLength(REACTION_EMOJIS.length);

    fireEvent.click(within(dialog).getByRole("button", { name: "React with rocket" }));
    await waitFor(() => expect(addSpy).toHaveBeenCalledWith("studio", "msg-1", "rocket"));

    const chip = await screen.findByRole("button", { name: "rocket reaction, 1" });
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(chip.getAttribute("title")).toBe("Lisi reacted with :rocket:");

    fireEvent.click(chip);
    await waitFor(() => expect(removeSpy).toHaveBeenCalledWith("studio", "msg-1", "rocket"));
    await waitFor(() => expect(screen.queryByRole("button", { name: "rocket reaction, 1" })).toBeNull());
  });

  it("supports reactions inside ThreadDetail messages as well", async () => {
    vi.spyOn(api, "listMessages").mockResolvedValue({
      messages: [
        makeMessage({
          id: "tr-1",
          conversation_id: "th-1",
          body_markdown: "Thread update",
          reactions: [{ emoji: "eyes", count: 1, member_ids: [lin.id], reacted: false }],
        }),
      ],
      read_seq: 0,
      following: true,
    });
    vi.spyOn(api, "listConversationMembers").mockResolvedValue({ members: [lisi, lin] });
    const addSpy = vi.spyOn(api, "addReaction").mockResolvedValue({
      reactions: [{ emoji: "eyes", count: 2, member_ids: [lin.id, lisi.id], reacted: true }],
    });

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <DetailPane
          spaceSlug="studio"
          boot={boot}
          target={{ kind: "thread", threadConvID: "th-1", rootMessageID: "msg-1", parentSlug: "engineering" }}
          onClose={() => {}}
          onSelectTarget={() => {}}
        />
      </QueryClientProvider>,
    );

    const chip = await screen.findByRole("button", { name: "eyes reaction, 1" });
    expect(chip.getAttribute("title")).toBe("Lin reacted with :eyes:");
    fireEvent.click(chip);

    await waitFor(() => expect(addSpy).toHaveBeenCalledWith("studio", "tr-1", "eyes"));
    const updated = await screen.findByRole("button", { name: "eyes reaction, 2" });
    expect(updated.getAttribute("title")).toBe("Lin, Lisi reacted with :eyes:");
    expect(updated.getAttribute("aria-pressed")).toBe("true");
  });
});
