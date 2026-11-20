import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { Member } from "../api";
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

    render(<MessageBody body={md} members={members} selfID={lisi.id} onSelectMember={onSelect} />);

    const buttons = screen.getAllByRole("button");
    expect(buttons).toHaveLength(1);
    expect(buttons[0].textContent).toBe("@lin");
    fireEvent.click(buttons[0]);
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
});
