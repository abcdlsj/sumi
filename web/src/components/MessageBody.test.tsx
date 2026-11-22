import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Member, Task } from "../api";
import { MessageBody, parseWorkspaceFilePath } from "./MessageBody";

afterEach(cleanup);

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

    expect(linBtn.className).toContain("bg-subtle");
    expect(linBtn.className).toContain("text-fg-2");
    expect(linBtn.className).not.toContain("text-accent-text");
    expect(lisiBtn.className).toContain("bg-accent-soft/65");
    expect(lisiBtn.className).toContain("text-fg");
    expect(lisiBtn.className).toContain("ring-1");
    expect(lisiBtn.className).not.toContain("text-accent-text");

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
    expect(allChip?.className).toContain("bg-accent-soft/65");
    expect(allChip?.className).toContain("text-fg");
    expect(allChip?.className).not.toContain("text-accent-text");
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

  it("renders CJK bold emphasis adjacent to Chinese punctuation", () => {
    const { container } = render(<MessageBody body="这是**重要（加粗）**说明。" members={members} selfID={lisi.id} />);
    const strong = container.querySelector("strong");
    expect(strong?.textContent).toBe("重要（加粗）");
  });

  it("parses safe workspace file paths and rejects URLs, traversal, secrets, and shell snippets", () => {
    expect(parseWorkspaceFilePath("notes.md")).toBe("notes.md");
    expect(parseWorkspaceFilePath("./docs/plan.md")).toBe("docs/plan.md");
    expect(parseWorkspaceFilePath("src/api.ts:42-50")).toBe("src/api.ts");
    expect(parseWorkspaceFilePath("src/api.ts#L12-L20")).toBe("src/api.ts");
    expect(parseWorkspaceFilePath("/Users/alice/.sumi/agents/a1/workspace/reports/out.md")).toBe("reports/out.md");
    expect(parseWorkspaceFilePath("Makefile")).toBe("Makefile");

    expect(parseWorkspaceFilePath("https://example.com/readme.md")).toBeNull();
    expect(parseWorkspaceFilePath("../secret.txt")).toBeNull();
    expect(parseWorkspaceFilePath(".env")).toBeNull();
    expect(parseWorkspaceFilePath(".ssh/id_rsa")).toBeNull();
    expect(parseWorkspaceFilePath("/etc/hosts")).toBeNull();
    expect(parseWorkspaceFilePath("console.log")).toBeNull();
    expect(parseWorkspaceFilePath("go test ./...")).toBeNull();
  });

  it("turns workspace file paths in agent messages into clickable triggers and leaves human messages plain", () => {
    const onOpen = vi.fn();
    const md = [
      "Updated `docs/plan.md:12`, wrote reports/summary.md, and linked [notes](./notes.md).",
      "External [site](https://example.com/a.md) and `console.log` stay normal.",
      "```ts",
      "import './ignored.ts';",
      "```",
    ].join("\n\n");

    const { rerender } = render(<MessageBody body={md} members={members} selfID={lisi.id} onOpenWorkspaceFile={onOpen} />);

    const codeLink = screen.getByRole("link", { name: "docs/plan.md:12" });
    expect(codeLink.className).toContain("underline");
    expect(codeLink.className).not.toContain("border");
    expect(codeLink.className).not.toContain("bg-subtle");
    expect(codeLink.className).not.toContain("inline-flex");
    fireEvent.click(codeLink);
    expect(onOpen).toHaveBeenLastCalledWith("docs/plan.md");

    fireEvent.click(screen.getByRole("link", { name: "reports/summary.md" }));
    expect(onOpen).toHaveBeenLastCalledWith("reports/summary.md");

    fireEvent.click(screen.getByRole("link", { name: "notes" }));
    expect(onOpen).toHaveBeenLastCalledWith("notes.md");

    expect(screen.queryByRole("link", { name: "console.log" })).toBeNull();
    expect(onOpen).toHaveBeenCalledTimes(3);

    rerender(<MessageBody body="Check `docs/plan.md` and reports/summary.md" members={members} selfID={lisi.id} />);
    expect(screen.queryByRole("link")).toBeNull();
  });
});
