import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Computer, type Member } from "../api";
import { AgentForm } from "./AgentForm";
import { identicon } from "./Avatar";

const computer: Computer = {
  id: "c1",
  space_id: "sp",
  name: "studio",
  hostname: "studio.local",
  os: "macos",
  connection_status: "online",
  acp_providers: ["pi"],
  created_at: "",
};
const created = { id: "a1", display_name: "Lin", kind: "agent" } as Member;

afterEach(() => vi.restoreAllMocks());

describe("AgentForm", () => {
  it("creates an ACP agent with the chosen provider and disables missing ones", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    const onCreated = vi.fn();
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={onCreated} />);
    expect((screen.getByRole("radio", { name: /Codex/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith("s", expect.objectContaining({ driver_kind: "acp", driver_config: { provider: "pi" }, computer_id: "c1" })),
    );
    expect(onCreated).toHaveBeenCalledWith(created);
  });

  it("validates the model API fields before calling the server", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: /Model API/ }));
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    expect(await screen.findByText(/Enter an http\(s\) URL/)).toBeTruthy();
    expect(create).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "http://127.0.0.1:4000/v1" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-5-mini" } });
    fireEvent.change(screen.getByPlaceholderText("sk-…"), { target: { value: "sk-test" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({ driver_kind: "builtin", driver_config: { base_url: "http://127.0.0.1:4000/v1", model: "gpt-5-mini", api_key: "sk-test" } }),
      ),
    );
  });
});

describe("identicon", () => {
  it("is deterministic, mirrored, and never empty", () => {
    for (const seed of ["a", "lin", "4f1c-uuid", ""]) {
      const a = identicon(seed);
      expect(identicon(seed)).toEqual(a);
      expect(a.cells.filter(Boolean).length).toBeGreaterThan(2);
      for (let r = 0; r < 5; r++) for (let c = 0; c < 5; c++) expect(a.cells[r * 5 + c]).toBe(a.cells[r * 5 + (4 - c)]);
    }
  });
});
