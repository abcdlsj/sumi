import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearAgentTemplatesForTests, listAgentTemplates, saveAgentTemplate, suggestCopyName } from "../agentTemplates";
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
const existingPiAgent: Member = {
  id: "a-lin",
  space_id: "s",
  kind: "agent",
  display_name: "Lin",
  access_level: "member",
  agent: {
    computer_id: "c1",
    role_text: "Backend reviewer",
    driver_kind: "acp",
    driver_config: {
      provider: "pi",
      pi_provider: "openai",
      pi_model: "gpt-5-mini",
      pi_packages: ["npm:custom-pi"],
    },
    runtime_state: "idle",
  },
  created_at: "",
};
const existingModelAgent: Member = {
  id: "a-api",
  space_id: "s",
  kind: "agent",
  display_name: "Sora",
  access_level: "member",
  agent: {
    computer_id: "c1",
    role_text: "Research analyst",
    driver_kind: "builtin",
    driver_config: {
      base_url: "http://127.0.0.1:4000/v1",
      model: "gpt-5-mini",
      api_key_set: true,
    },
    runtime_state: "idle",
  },
  created_at: "",
};

afterEach(() => {
  clearAgentTemplatesForTests();
  vi.restoreAllMocks();
});

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

  it("creates an ACP agent with a custom command override under Advanced", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    // Custom command is hidden behind Advanced by default.
    expect(screen.queryByLabelText(/Custom command/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    fireEvent.change(screen.getByLabelText(/Custom command/), { target: { value: "my-acp --stdio" } });
    // Entering a custom command unlocks providers whose default binary is absent on PATH.
    const claude = screen.getByRole("radio", { name: /Claude Code/ }) as HTMLButtonElement;
    expect(claude.disabled).toBe(false);
    fireEvent.click(claude);
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({ driver_kind: "acp", driver_config: { provider: "claude", command: ["my-acp", "--stdio"] }, computer_id: "c1" }),
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
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    fireEvent.change(screen.getByLabelText(/pi provider/), { target: { value: " custom-pi " } });
    fireEvent.change(screen.getByLabelText(/pi model/), { target: { value: "model-fast" } });
    fireEvent.change(screen.getByLabelText(/pi packages/), { target: { value: "npm:custom-pi-provider , ," } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({
          driver_config: {
            provider: "pi",
            pi_provider: "custom-pi",
            pi_model: "model-fast",
            pi_packages: ["npm:custom-pi-provider"],
          },
        }),
      ),
    );
  });

  it("forwards a context window and rejects one outside the Claude Code range", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: /Advanced/ }));
    fireEvent.change(screen.getByLabelText(/Custom command/), { target: { value: "ak my-claude" } });
    fireEvent.click(screen.getByRole("radio", { name: /Claude Code/ }));
    fireEvent.change(screen.getByLabelText(/Context window/), { target: { value: "64000" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    expect(await screen.findByText(/between 100000 and 1000000/)).toBeTruthy();
    expect(create).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText(/Context window/), { target: { value: "200000" } });
    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({ driver_config: { provider: "claude", command: ["ak", "my-claude"], context_window: 200000 } }),
      ),
    );
  });

  it("prefills from an existing agent copy with a unique name and copies driver settings", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(
      <AgentForm
        spaceSlug="s"
        computers={[computer]}
        initialAgent={existingPiAgent}
        existingMembers={[existingPiAgent, { ...existingPiAgent, id: "a-lin2", display_name: "Lin_2" }]}
        onCreated={() => {}}
      />,
    );

    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Lin_3");
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Backend reviewer");
    expect((screen.getByLabelText(/pi provider/) as HTMLInputElement).value).toBe("openai");
    expect((screen.getByLabelText(/pi model/) as HTMLInputElement).value).toBe("gpt-5-mini");

    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith("s", {
        display_name: "Lin_3",
        role_text: "Backend reviewer",
        computer_id: "c1",
        driver_kind: "acp",
        driver_config: {
          provider: "pi",
          pi_provider: "openai",
          pi_model: "gpt-5-mini",
          pi_packages: ["npm:custom-pi"],
        },
      }),
    );
  });

  it("copies a Model API agent in-form without copying its secret API key", () => {
    render(<AgentForm spaceSlug="s" computers={[computer]} existingMembers={[existingPiAgent, existingModelAgent]} onCreated={() => {}} />);

    fireEvent.click(screen.getByRole("button", { name: /Copy from agent/ }));
    fireEvent.click(screen.getByRole("option", { name: /Sora/ }));

    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Sora_2");
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Research analyst");
    expect((screen.getByLabelText("Base URL") as HTMLInputElement).value).toBe("http://127.0.0.1:4000/v1");
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("gpt-5-mini");
    expect((screen.getByPlaceholderText("sk-…") as HTMLInputElement).value).toBe("");
    expect(screen.getByText(/API key from/i)).toBeTruthy();
  });

  it("saves a creation config as a template without API keys, applies it in a fresh form, and deletes it", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    const first = render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);

    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "Security auditor" } });
    fireEvent.click(screen.getByRole("button", { name: /Model API/ }));
    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "http://127.0.0.1:4000/v1" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-5-mini" } });
    fireEvent.change(screen.getByPlaceholderText("sk-…"), { target: { value: "sk-never-store" } });

    fireEvent.click(screen.getByRole("button", { name: "Save as template" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Template name" }), { target: { value: "Auditor" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    const saved = listAgentTemplates();
    expect(saved).toHaveLength(1);
    expect(saved[0].name).toBe("Auditor");
    expect(JSON.stringify(saved[0])).not.toContain("sk-never-store");

    first.unmount();

    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: /^Auditor/ }));
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Security auditor");
    expect((screen.getByLabelText("Base URL") as HTMLInputElement).value).toBe("http://127.0.0.1:4000/v1");
    expect((screen.getByPlaceholderText("sk-…") as HTMLInputElement).value).toBe("");

    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({
          role_text: "Security auditor",
          driver_kind: "builtin",
          driver_config: { base_url: "http://127.0.0.1:4000/v1", model: "gpt-5-mini" },
        }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Delete template Auditor" }));
    expect(listAgentTemplates()).toHaveLength(0);
  });

  it("saves a template with an empty role", () => {
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);

    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save as template" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Template name" }), { target: { value: "Bare" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(listAgentTemplates()).toHaveLength(1);
    expect(listAgentTemplates()[0].role_text).toBe("");
  });

  it("keeps current role and driver values when a template leaves them blank", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    saveAgentTemplate({ name: "Bare", role_text: "", driver_kind: "builtin", driver_config: {} });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);

    fireEvent.click(screen.getByRole("button", { pressed: false, name: /Model API/ }));
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "Keep my role" } });
    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "http://127.0.0.1:4000/v1" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "keep-model" } });

    fireEvent.click(screen.getByRole("button", { name: /^Bare/ }));
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Keep my role");
    expect((screen.getByLabelText("Base URL") as HTMLInputElement).value).toBe("http://127.0.0.1:4000/v1");
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("keep-model");

    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({
          role_text: "Keep my role",
          driver_kind: "builtin",
          driver_config: { base_url: "http://127.0.0.1:4000/v1", model: "keep-model" },
        }),
      ),
    );
  });

  it("edits a saved template via the edit modal and via the active-template quick update without creating an agent", () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(<AgentForm spaceSlug="s" computers={[computer]} onCreated={() => {}} />);

    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "Security auditor" } });
    fireEvent.click(screen.getByRole("button", { name: /Model API/ }));
    fireEvent.change(screen.getByLabelText("Base URL"), { target: { value: "http://127.0.0.1:4000/v1" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "gpt-5-mini" } });

    fireEvent.click(screen.getByRole("button", { name: "Save as template" }));
    fireEvent.change(screen.getByRole("textbox", { name: "Template name" }), { target: { value: "Auditor" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const originalID = listAgentTemplates()[0].id;

    // Edit via modal: rename + update role + update model
    fireEvent.click(screen.getByRole("button", { name: "Edit template Auditor" }));
    const dialog = screen.getByRole("dialog", { name: "Edit template" });
    const nameInputEl = dialog.querySelector('input[required]') as HTMLInputElement;
    fireEvent.change(nameInputEl, { target: { value: "AppSec Auditor" } });
    const roleArea = dialog.querySelector("textarea") as HTMLTextAreaElement;
    fireEvent.change(roleArea, { target: { value: "Deep AppSec reviewer" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(create).not.toHaveBeenCalled();
    const afterModal = listAgentTemplates();
    expect(afterModal).toHaveLength(1);
    expect(afterModal[0].id).toBe(originalID);
    expect(afterModal[0].name).toBe("AppSec Auditor");
    expect(afterModal[0].role_text).toBe("Deep AppSec reviewer");
    // Active form also refreshed to match edited template
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Deep AppSec reviewer");

    // Quick-update active template directly from form edits
    fireEvent.change(screen.getByLabelText("Role"), { target: { value: "Updated inline role" } });
    fireEvent.click(screen.getByRole("button", { name: /Update “AppSec Auditor”/ }));
    expect(listAgentTemplates()[0].role_text).toBe("Updated inline role");
  });

  it("prefills from an agent.create action card proposal and sends action_card_message_id on create", async () => {
    const create = vi.spyOn(api, "createAgent").mockResolvedValue({ agent: created });
    render(
      <AgentForm
        spaceSlug="s"
        computers={[computer]}
        initialProposal={{
          message_id: "m-card-42",
          conversation_id: "c-eng",
          proposed_by_name: "Lin",
          params: {
            display_name: "XiaoZhu",
            role_text: "Helper engineer",
            driver_kind: "acp",
            driver_config: { provider: "pi" },
          },
        }}
        onCreated={() => {}}
      />,
    );

    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("XiaoZhu");
    expect((screen.getByLabelText("Role") as HTMLTextAreaElement).value).toBe("Helper engineer");

    fireEvent.click(screen.getByRole("button", { name: "Create agent" }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        "s",
        expect.objectContaining({
          display_name: "XiaoZhu",
          role_text: "Helper engineer",
          action_card_message_id: "m-card-42",
        }),
      ),
    );
  });
});

describe("suggestCopyName", () => {
  it("increments suffixes, avoids case-insensitive collisions, and respects the 32-char limit", () => {
    expect(suggestCopyName("Lin", ["Lin"])).toBe("Lin_2");
    expect(suggestCopyName("Lin_2", ["lin", "LIN_2"])).toBe("Lin_3");
    const longName = "a".repeat(32);
    const copy = suggestCopyName(longName, [longName]);
    expect(copy.length).toBeLessThanOrEqual(32);
    expect(copy.endsWith("_2")).toBe(true);
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
