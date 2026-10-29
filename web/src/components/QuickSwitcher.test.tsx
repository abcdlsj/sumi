import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { QuickSwitcher, type QuickSwitcherItem } from "./QuickSwitcher";

const items: QuickSwitcherItem[] = [
  { id: "general", label: "general", group: "Channels", leading: "#", href: "/s/lab/channels/general" },
  { id: "design", label: "design", group: "Channels", leading: "#", href: "/s/lab/channels/design" },
  { id: "tasks", label: "Tasks", group: "Go to", leading: "", href: "/s/lab/tasks" },
];

afterEach(() => cleanup());

describe("QuickSwitcher", () => {
  it("filters by name and opens the highlighted result with the keyboard", () => {
    const open = vi.fn();
    render(<QuickSwitcher items={items} close={() => {}} open={open} />);

    const input = screen.getByRole("combobox", { name: "Jump to" });
    fireEvent.change(input, { target: { value: "s" } });
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["#design", "Tasks"]);

    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(open).toHaveBeenCalledWith("/s/lab/tasks");
  });

  it("says when nothing matches", () => {
    render(<QuickSwitcher items={items} close={() => {}} open={() => {}} />);
    fireEvent.change(screen.getByRole("combobox", { name: "Jump to" }), { target: { value: "zzz" } });
    expect(screen.getByText(/No matches/)).toBeVisible();
  });
});
