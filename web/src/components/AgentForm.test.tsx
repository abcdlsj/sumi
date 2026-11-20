import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Computer, type Member } from "../api";
import { AgentForm } from "./AgentForm";
import { agentSprite } from "./Avatar";

const computer: Computer = {
  id: "c1",
  owner_member_id: "m",
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

  it("creates an ACP agent with a custom command override", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    fireEvent.change(screen.getByLabelText(/Custom command/), { target: { value: "ak acp claude" } });
    // Entering a custom command unlocks providers whose default binary is absent on PATH.
    const claude = screen.getByRole("radio", { name: /Claude Code/ }) as HTMLButtonElement;
    expect(claude.disabled).toBe(false);
    fireEvent.click(claude);
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({ driver_kind: "acp", driver_config: { provider: "claude", command: ["ak", "acp", "claude"] }, computer_id: "c1" }),
      ),
    );
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

  it("pins an agent to a pi provider and forwards only requested packages", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    fireEvent.change(screen.getByLabelText(/pi provider/), { target: { value: " ak-opendesign-pi " } });
    fireEvent.change(screen.getByLabelText(/pi model/), { target: { value: "deepseek-v4.1-flash" } });
    fireEvent.change(screen.getByLabelText(/pi packages/), { target: { value: "npm:pi-commandcode-provider , ," } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({
          driver_config: {
            provider: "pi",
            pi_provider: "ak-opendesign-pi",
            pi_model: "deepseek-v4.1-flash",
            pi_packages: ["npm:pi-commandcode-provider"],
          },
        }),
      ),
    );
  });
});

describe("agentSprite", () => {
  it("is deterministic, mirrored, and always has a body with eyes", () => {
    for (const seed of ["a", "lin", "4f1c-uuid", "", ...Array.from({ length: 200 }, (_, i) => `seed-${i}`)]) {
      const a = agentSprite(seed);
      expect(agentSprite(seed)).toEqual(a);
      const flat = a.cells.flat();
      expect(flat.filter((v) => v === 1).length).toBeGreaterThan(5);
      expect(flat).toContain(3);
      for (const row of a.cells) for (let x = 0; x < 8; x++) expect(row[x]).toBe(row[7 - x]);
    }
  });

  it("never paints a violet or magenta sprite", () => {
    const hues = new Set<number>();
    for (let i = 0; i < 500; i++) {
      const { hue } = agentSprite(`seed-${i}`);
      expect(hue >= 340 || hue < 235).toBe(true);
      hues.add(hue);
    }
    expect(hues.size).toBeGreaterThanOrEqual(10);
  });
});
