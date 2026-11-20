import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, formatClock, type BootstrapData, type Conversation, type Member, type Message } from "../api";
import { ConversationView } from "./ConversationView";

const lisi: Member = { id: "m-lisi", space_id: "s", kind: "human", display_name: "Lisi", access_level: "owner", created_at: "2026-11-19T10:00:00Z" };
const sam: Member = { id: "m-sam", space_id: "s", kind: "human", display_name: "Sam", access_level: "member", created_at: "2026-11-19T10:00:00Z" };
const lin: Member = {
  id: "m-lin",
  space_id: "s",
  kind: "agent",
  display_name: "Lin",
  access_level: "member",
  agent: { role_text: "Backend engineer", driver_kind: "acp", runtime_state: "idle" },
  created_at: "2026-11-19T10:00:00Z",
};

function channel(id: string, slug: string): Conversation {
  return {
    id,
    space_id: "s",
    kind: "public",
    slug,
    next_seq: 1,
    read_seq: 0,
    unread_count: 0,
    mention_count: 0,
    notify_level: "mentions",
    following: false,
  };
}

const conv = channel("conv-eng", "engineering");

const boot: BootstrapData = {
  space: { id: "s", slug: "studio", name: "Studio", accent: "flame", owner_member_id: lisi.id, event_seq: 1, created_at: "2026-11-19T10:00:00Z" },
  current_member: lisi,
  members: [lisi, lin, sam],
  conversations: [conv],
  inbox: [],
  tasks: [],
  computers: [],
};

function msg(seq: number, author: Member = sam, body = `message ${seq}`): Message {
  return {
    id: `msg-${seq}`,
    space_id: "s",
    conversation_id: conv.id,
    seq,
    author_member_id: author.id,
    author_display_name: author.display_name,
    author_kind: author.kind,
    kind: "text",
    body_markdown: body,
    created_at: new Date(Date.UTC(2026, 10, 19, 10, seq)).toISOString(),
  };
}

let messages: Message[] = [];
let readSeq = 0;

function sizeStream(el: HTMLElement, height: number) {
  Object.defineProperty(el, "scrollHeight", { value: height, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: 100, configurable: true });
}

function stream(container: HTMLElement) {
  const el = container.querySelector("div.overflow-y-auto.scroll-thin");
  if (!el) throw new Error("message stream not found");
  return el as HTMLElement;
}

function renderView(conversation: Conversation = conv, data: BootstrapData = boot) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onOpenDetail = vi.fn();
  const view = render(
    <QueryClientProvider client={qc}>
      <ConversationView spaceSlug="studio" boot={data} conv={conversation} onOpenDetail={onOpenDetail} />
    </QueryClientProvider>,
  );
  return { ...view, qc, onOpenDetail, box: stream(view.container) };
}

/** Delivers messages the way an SSE-driven refetch would. */
async function arrive(qc: QueryClient, el: HTMLElement, next: Message[], height: number) {
  messages = next;
  sizeStream(el, height);
  await act(async () => {
    await qc.invalidateQueries({ queryKey: ["messages", "studio", conv.id] });
  });
}

beforeEach(() => {
  messages = [msg(1), msg(2)];
  readSeq = 0;
  vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages, read_seq: readSeq, following: false }));
  vi.spyOn(api, "listConversationMembers").mockResolvedValue({ members: [lin, sam] });
  vi.spyOn(api, "sendMessage").mockImplementation(async (_s, _c, body) => ({
    message: { ...msg(messages.length + 1, lisi, body), id: "msg-sent" },
  }));
});

afterEach(() => vi.restoreAllMocks());

describe("message stream", () => {
  it("opens at the end and follows new messages", async () => {
    const { box, qc } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 2");
    expect(box.scrollTop).toBe(600);

    await arrive(qc, box, [msg(1), msg(2), msg(3)], 700);
    expect(await screen.findByText("message 3")).toBeTruthy();
    expect(box.scrollTop).toBe(700);
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });

  it("offers a jump pill instead of yanking a reader who scrolled up", async () => {
    const { box, qc } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 2");

    box.scrollTop = 80;
    fireEvent.scroll(box);
    await arrive(qc, box, [msg(1), msg(2), msg(3)], 700);

    expect(box.scrollTop).toBe(80);
    const pill = await screen.findByRole("button", { name: /1 new message/ });
    fireEvent.click(pill);
    expect(box.scrollTop).toBe(700);
    await waitFor(() => expect(screen.queryByRole("button", { name: /new message/ })).toBeNull());
  });

  it("always follows the message the viewer just sent", async () => {
    const { box, qc } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 2");

    box.scrollTop = 80;
    fireEvent.scroll(box);
    fireEvent.change(screen.getByRole("textbox", { name: "Message #engineering" }), { target: { value: "on it" } });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Message #engineering" }), { key: "Enter" });

    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    await arrive(qc, box, [msg(1), msg(2), msg(3, lisi, "on it")], 700);
    expect(box.scrollTop).toBe(700);
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });

  it("opens a conversation with unread messages on the persisted divider", async () => {
    readSeq = 1;
    const { box } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 2");
    expect(screen.getByText("New messages")).toBeTruthy();
    expect(box.scrollTop).toBe(0);
  });

  it("keeps the divider when a later fetch advances the watermark", async () => {
    readSeq = 1;
    const { box, qc } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 2");
    expect(screen.getByText("New messages")).toBeTruthy();

    // Opening marked everything read; the divider of this visit must not vanish.
    readSeq = 2;
    await arrive(qc, box, [msg(1), msg(2), msg(3)], 700);
    expect(await screen.findByText("message 3")).toBeTruthy();
    expect(screen.getByText("New messages")).toBeTruthy();
    expect(await screen.findByRole("button", { name: /1 new message/ })).toBeTruthy();
  });

  it("loads earlier messages when has_more is true without triggering the new-messages pill", async () => {
    vi.spyOn(api, "listMessages").mockImplementation(async (_s, _c, _m, opts) => {
      if (opts?.beforeSeq === 3) {
        return { messages: [msg(1), msg(2)], read_seq: 0, following: false, has_more: false };
      }
      return { messages: [msg(3), msg(4)], read_seq: 0, following: false, has_more: true };
    });
    const { box } = renderView();
    sizeStream(box, 600);
    await screen.findByText("message 4");

    const loadBtn = screen.getByRole("button", { name: "Load earlier messages" });
    fireEvent.click(loadBtn);
    expect(await screen.findByText("message 1")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Load earlier messages" })).toBeNull();
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });
});

describe("composer", () => {
  it("keeps focus after sending and clears the composer", async () => {
    const view = renderView();
    const box = await screen.findByRole("textbox", { name: "Message #engineering" });
    fireEvent.change(box, { target: { value: "shipping it" } });
    fireEvent.keyDown(box, { key: "Enter" });
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalled());
    await waitFor(() => expect((box as HTMLTextAreaElement).value).toBe(""));
    expect(document.activeElement).toBe(box);
    view.unmount();
  });

  it("keeps a draft per conversation", async () => {
    const first = renderView();
    const firstBox = await screen.findByRole("textbox", { name: "Message #engineering" });
    fireEvent.change(firstBox, { target: { value: "half-written" } });
    first.unmount();

    const other = renderView(channel("conv-design", "design"));
    expect((await screen.findByRole("textbox", { name: "Message #design" }) as HTMLTextAreaElement).value).toBe("");
    other.unmount();

    renderView();
    await waitFor(() =>
      expect((screen.getByRole("textbox", { name: "Message #engineering" }) as HTMLTextAreaElement).value).toBe("half-written"),
    );
  });

  it("cancels a reply with Escape", async () => {
    const { container } = renderView();
    await screen.findByText("message 2");
    const row = screen.getByText("message 2").closest("article")!;
    fireEvent.click(within(row as HTMLElement).getByRole("button", { name: "Reply" }));
    expect(screen.getByText("Replying in thread to")).toBeTruthy();
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Reply in thread" }), { key: "Escape" });
    expect(screen.queryByText("Replying in thread to")).toBeNull();
    expect(container).toBeTruthy();
  });
});

describe("@mention autocomplete", () => {
  it("offers agents first and inserts the picked name without sending", async () => {
    renderView();
    await screen.findByText("message 2");
    const box = screen.getByRole("textbox", { name: "Message #engineering" });
    fireEvent.change(box, { target: { value: "@" } });

    const list = await screen.findByRole("listbox", { name: "Mention candidates" });
    const options = within(list).getAllByRole("option");
    expect(options).toHaveLength(2);
    expect(options[0].textContent).toContain("Lin");
    expect(options[0].textContent).toContain("Agent");
    expect(options[1].textContent).toContain("Sam");
    expect(options[1].textContent).toContain("member");

    fireEvent.keyDown(box, { key: "ArrowDown" });
    expect(within(list).getAllByRole("option")[1].getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(box, { key: "Enter" });

    expect((box as HTMLTextAreaElement).value).toBe("@Sam ");
    expect(api.sendMessage).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(box);
  });

  it("filters as you type and disappears without a match", async () => {
    renderView();
    await screen.findByText("message 2");
    const box = screen.getByRole("textbox", { name: "Message #engineering" });

    fireEvent.change(box, { target: { value: "@n" } });
    const list = await screen.findByRole("listbox", { name: "Mention candidates" });
    const options = within(list).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0].textContent).toContain("Lin");

    fireEvent.change(box, { target: { value: "@zzz" } });
    await waitFor(() => expect(screen.queryByRole("listbox", { name: "Mention candidates" })).toBeNull());
    fireEvent.keyDown(box, { key: "Escape" });
  });
});

describe("channel header", () => {
  it("keeps a compact members entry with the channel size", async () => {
    const { onOpenDetail } = renderView();
    await screen.findByText("message 2");
    const entry = await screen.findByRole("button", { name: "Members" });
    expect(entry.getAttribute("title")).toBe("Members");
    expect(within(entry).getByText("2")).toBeTruthy();
    expect(entry.querySelectorAll("svg")).toHaveLength(1);
    fireEvent.click(entry);
    expect(onOpenDetail).toHaveBeenCalledWith({ kind: "members", convID: conv.id });
  });
});

describe("@mention highlights in messages", () => {
  it("opens agent or human detail when clicking a highlighted @mention", async () => {
    messages = [msg(1, lisi, "cc @Lin and @Sam for review")];
    const { onOpenDetail } = renderView();

    const agentMention = await screen.findByRole("button", { name: "@Lin" });
    fireEvent.click(agentMention);
    expect(onOpenDetail).toHaveBeenCalledWith({ kind: "agent", memberID: lin.id });

    const humanMention = screen.getByRole("button", { name: "@Sam" });
    fireEvent.click(humanMention);
    expect(onOpenDetail).toHaveBeenCalledWith({ kind: "human", memberID: sam.id });
  });
});

describe("thread reply summary", () => {
  it("shows the newest replies under the root message and opens the thread", async () => {
    messages = [
      {
        ...msg(1, lisi, "rollout plan?"),
        reply_count: 7,
        thread_conversation_id: "th-1",
        last_reply_at: new Date(Date.UTC(2026, 10, 19, 10, 9)).toISOString(),
        latest_replies: [
          {
            message_id: "r-1",
            seq: 2,
            author_member_id: lin.id,
            author_display_name: lin.display_name,
            author_kind: "agent",
            preview: "canary first",
            created_at: new Date(Date.UTC(2026, 10, 19, 10, 5)).toISOString(),
          },
          {
            message_id: "r-2",
            seq: 3,
            author_member_id: sam.id,
            author_display_name: sam.display_name,
            author_kind: "human",
            preview: "then the rest",
            created_at: new Date(Date.UTC(2026, 10, 19, 10, 9)).toISOString(),
          },
        ],
      },
    ];
    const { onOpenDetail } = renderView();

    const summary = await screen.findByRole("button", { name: /7 replies/ });
    expect(within(summary).getByText("canary first")).toBeTruthy();
    expect(within(summary).getByText("then the rest")).toBeTruthy();
    expect(within(summary).getByText("Lin")).toBeTruthy();
    expect(within(summary).getByText(`Last ${formatClock(new Date(Date.UTC(2026, 10, 19, 10, 9)).toISOString())}`)).toBeTruthy();

    fireEvent.click(within(summary).getByText("then the rest"));
    expect(onOpenDetail).toHaveBeenCalledWith({ kind: "thread", threadConvID: "th-1", rootMessageID: "msg-1", parentSlug: "engineering" });
  });

  it("keeps the count when a thread has no text replies to preview", async () => {
    messages = [{ ...msg(1, lisi, "see thread"), reply_count: 2, thread_conversation_id: "th-2" }];
    renderView();
    const summary = await screen.findByRole("button", { name: /2 replies/ });
    expect(within(summary).queryAllByRole("img")).toHaveLength(0);
  });
});
