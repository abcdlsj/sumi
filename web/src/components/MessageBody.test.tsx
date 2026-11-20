import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Member, Task } from "../api";
import { MessageBody } from "./MessageBody";

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
const lin2: Member = {
  id: "m-lin2",
  space_id: "s",
  kind: "agent",
  display_name: "Lin_2",
  access_level: "member",
  agent: { role_text: "Reviewer", driver_kind: "acp", runtime_state: "idle" },
  created_at: "2026-11-19T10:00:00Z",
};
const members = [lisi, sam, lin, lin2];

describe("MessageBody @mentions", () => {
  it("highlights known mentions case-insensitively and dispatches clicks", () => {
    const onSelect = vi.fn();
    render(<MessageBody body="Hey @lin and @SAM, please check with @Lin_2 and @lisi." members={members} selfID={lisi.id} onSelectMember={onSelect} />);

    const linBtn = screen.getByRole("button", { name: "@lin" });
    const samBtn = screen.getByRole("button", { name: "@SAM" });
    const lin2Btn = screen.getByRole("button", { name: "@Lin_2" });
    const lisiBtn = screen.getByRole("button", { name: "@lisi" });

    expect(linBtn.className).toContain("bg-accent-soft");
    expect(lisiBtn.className).toContain("ring-1");

    fireEvent.click(linBtn);
    expect(onSelect).toHaveBeenCalledWith(lin);

    fireEvent.click(samBtn);
    expect(onSelect).toHaveBeenCalledWith(sam);

    fireEvent.click(lin2Btn);
    expect(onSelect).toHaveBeenCalledWith(lin2);
  });

  it("ignores code blocks, inline code, links, emails, prefixes and unknown handles", () => {
    const onSelect = vi.fn();
    const md = [
      "Plain @unknown and @linear and email foo@lin.dev.",
      "Inline `@lin` and link [ping @lin](https://example.com/@lin).",
      "```",
      "@lin inside fence",
      "```",
      "Chinese boundary: 请@lin看一下。",
    ].join("\n\n");

    const { container } = render(<MessageBody body={md} members={members} selfID={lisi.id} onSelectMember={onSelect} />);

    const mentions = container.querySelectorAll("[data-mention]");
    expect(mentions).toHaveLength(1);
    expect(mentions[0].textContent).toBe("@lin");
    fireEvent.click(mentions[0]);
    expect(onSelect).toHaveBeenCalledWith(lin);
  });

  it("highlights @all as a non-clickable badge", () => {
    const onSelect = vi.fn();
    const { container } = render(<MessageBody body="Heads up @all" members={members} selfID={lisi.id} onSelectMember={onSelect} />);

    expect(screen.queryByRole("button")).toBeNull();
    const allChip = container.querySelector('[data-mention="all"]');
    expect(allChip?.textContent).toBe("@all");
    expect(allChip?.className).toContain("bg-accent-soft");
  });

  it("highlights known #N task refs, skips code/unknown refs, and copies fenced code blocks", async () => {
    const task14: Task = {
      id: "t-14",
      space_id: "s",
      number: 14,
      channel_id: "c1",
      title: "Fix pagination",
      description: "",
      status: "in_progress",
      creator_member_id: lisi.id,
      revision: 1,
      message_count: 0,
      created_at: "2026-11-19T10:00:00Z",
      updated_at: "2026-11-19T10:00:00Z",
    };
    const onSelectTask = vi.fn();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    const md = "See #14 and ignore #999 and `#14`.\n\n```sh\nsumi task show t-14\n```";
    render(<MessageBody body={md} members={members} tasks={[task14]} selfID={lisi.id} onSelectTask={onSelectTask} />);

    const taskBtn = screen.getByRole("button", { name: "#14" });
    expect(taskBtn.getAttribute("title")).toBe("#14 Fix pagination");
    fireEvent.click(taskBtn);
    expect(onSelectTask).toHaveBeenCalledWith(task14);

    const copyBtn = screen.getByRole("button", { name: "Copy code" });
    fireEvent.click(copyBtn);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("sumi task show t-14\n"));
  });
});
