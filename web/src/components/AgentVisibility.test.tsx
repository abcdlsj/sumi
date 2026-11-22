import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useRef, useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { BootstrapData, Member, Message } from "../api";
import { AgentReadReceipts, buildAgentReadMap, ThreadOutlineRail, WorkingAgentsPopover } from "./AgentVisibility";
import type { DetailTarget } from "./DetailPane";

afterEach(cleanup);

const lisi: Member = { id: "m-lisi", space_id: "s", kind: "human", display_name: "Lisi", access_level: "owner", created_at: "2026-11-19T10:00:00Z" };
const lin: Member = {
  id: "m-lin",
  space_id: "s",
  kind: "agent",
  display_name: "Lin",
  access_level: "member",
  agent: {
    role_text: "Backend engineer",
    driver_kind: "acp",
    runtime_state: "working",
    focus_conversation_id: "c-eng",
    activity: { tool: "execute" },
  },
  created_at: "2026-11-19T10:00:00Z",
};
const kira: Member = {
  id: "m-kira",
  space_id: "s",
  kind: "agent",
  display_name: "Kira",
  access_level: "member",
  agent: { role_text: "Reviewer", driver_kind: "acp", runtime_state: "idle" },
  created_at: "2026-11-19T10:00:00Z",
};

const boot: BootstrapData = {
  space: { id: "s", slug: "studio", name: "Studio", accent: "flame", owner_member_id: lisi.id, event_seq: 1, created_at: "2026-11-19T10:00:00Z" },
  current_member: lisi,
  members: [lisi, lin, kira],
  conversations: [
    {
      id: "c-eng",
      space_id: "s",
      kind: "public",
      slug: "engineering",
      next_seq: 5,
      read_seq: 4,
      unread_count: 0,
      mention_count: 0,
      following: false,
    },
  ],
  inbox: [],
  tasks: [],
  computers: [],
};

function makeMsg(seq: number, author: Member, body: string): Message {
  return {
    id: `m-${seq}`,
    space_id: "s",
    conversation_id: "c-eng",
    seq,
    author_member_id: author.id,
    author_display_name: author.display_name,
    author_kind: author.kind,
    kind: "text",
    body_markdown: body,
    created_at: new Date(Date.UTC(2026, 10, 19, 10, seq)).toISOString(),
  };
}

function PopoverHarness({
  onOpenConv,
  onOpenDetail,
}: {
  onOpenConv: (id: string) => void;
  onOpenDetail: (t: DetailTarget) => void;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  return (
    <>
      <button ref={triggerRef} type="button" onClick={() => setOpen((v) => !v)}>
        Open working
      </button>
      <WorkingAgentsPopover
        boot={boot}
        agents={[lin]}
        open={open}
        triggerRef={triggerRef}
        onClose={() => setOpen(false)}
        onOpenConversation={onOpenConv}
        onOpenDetail={onOpenDetail}
      />
    </>
  );
}

describe("AgentVisibility", () => {
  it("opens WorkingAgentsPopover with activity and jumps to the agent's focus conversation", () => {
    const onOpenConv = vi.fn();
    const onOpenDetail = vi.fn();
    render(<PopoverHarness onOpenConv={onOpenConv} onOpenDetail={onOpenDetail} />);

    fireEvent.click(screen.getByRole("button", { name: "Open working" }));

    const dialog = screen.getByRole("dialog", { name: "Working now" });
    expect(within(dialog).getByText("Running execute")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Lin" }));
    expect(onOpenConv).toHaveBeenCalledWith("c-eng");
  });

  it("places each agent read receipt on the latest message at or below its read_seq as quiet text", () => {
    const msgs = [makeMsg(1, lisi, "one"), makeMsg(2, lisi, "two"), makeMsg(3, lisi, "three")];
    const map = buildAgentReadMap(
      msgs,
      [
        { member_id: lin.id, read_seq: 2 },
        { member_id: kira.id, read_seq: 5 },
      ],
      [lisi, lin, kira],
    );
    expect(map.get("m-1")).toBeUndefined();
    expect(map.get("m-2")?.map((m) => m.id)).toEqual([lin.id]);
    expect(map.get("m-3")?.map((m) => m.id)).toEqual([kira.id]);

    const onSelect = vi.fn();
    render(<AgentReadReceipts agents={map.get("m-2")} boot={boot} onSelectAgent={onSelect} />);
    const receipts = screen.getByTestId("agent-read-receipts");
    expect(receipts.getAttribute("aria-label")).toContain("Lin");
    expect(within(receipts).queryByRole("img")).toBeNull();
    fireEvent.click(within(receipts).getByRole("button", { name: "Lin" }));
    expect(onSelect).toHaveBeenCalledWith(lin.id);
  });

  it("omits an agent's read receipt on its own message while keeping other agents who read it", () => {
    const msgs = [makeMsg(1, lisi, "one"), makeMsg(2, lin, "reply from lin")];
    const map = buildAgentReadMap(
      msgs,
      [
        { member_id: lin.id, read_seq: 2 },
        { member_id: kira.id, read_seq: 2 },
      ],
      [lisi, lin, kira],
    );
    expect(map.get("m-1")).toBeUndefined();
    expect(map.get("m-2")?.map((m) => m.id)).toEqual([kira.id]);
  });

  it("renders ThreadOutlineRail for threads with multiple human checkpoints and jumps on click", () => {
    const msgs = [
      makeMsg(1, lisi, "Initial question"),
      makeMsg(2, lin, "Long agent analysis"),
      makeMsg(3, lisi, "Follow-up request"),
    ];
    const onJump = vi.fn();
    render(<ThreadOutlineRail messages={msgs} selfID={lisi.id} onJump={onJump} />);

    const toggle = screen.getByRole("button", { name: "Thread outline" });
    fireEvent.click(toggle);
    const nav = screen.getByRole("navigation", { name: "Thread outline" });
    expect(within(nav).getByText("Initial question")).toBeTruthy();
    expect(within(nav).queryByText("Long agent analysis")).toBeNull();
    fireEvent.click(within(nav).getByText("Follow-up request"));
    expect(onJump).toHaveBeenCalledWith("m-3");
  });
});
