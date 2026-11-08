import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import type { Computer } from "../api/types";
import { AgentSetupFields } from "./AgentSetupFields";

afterEach(() => cleanup());

const spaceId = "019c0000-0000-7000-8000-000000000001";
const computer: Computer = {
  id: "019c0000-0000-7000-8000-000000000090",
  space_id: spaceId,
  name: "Studio",
  hostname: "studio.local",
  os: "macos",
  status: "online",
  daemon_version: "0.1.0",
  last_seen_at: "2026-07-25T00:00:00Z",
  created_at: "2026-07-25T00:00:00Z",
};

describe("Agent setup fields", () => {
  it("submits a Computer that came online while the dialog was open", () => {
    const submitted: Array<Record<string, string>> = [];
    const form = (computers: Computer[]) => (
      <form
        onSubmit={(event) => {
          event.preventDefault();
          submitted.push(Object.fromEntries(new FormData(event.currentTarget)) as Record<string, string>);
        }}
      >
        <AgentSetupFields computers={computers} isOwner />
        <button type="submit">Create Agent</button>
      </form>
    );

    const { rerender } = render(form([]));
    rerender(form([computer]));
    fireEvent.click(screen.getByRole("button", { name: "Create Agent" }));

    expect(submitted).toEqual([
      { computer_id: computer.id, driver: "codex", access_level: "member" },
    ]);
  });
});
