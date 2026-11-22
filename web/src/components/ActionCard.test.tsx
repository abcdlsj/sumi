import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Message } from "../api";
import { ActionCard, parseActionCard } from "./ActionCard";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

function makeCardMsg(payload: Record<string, unknown>): Message {
  return {
    id: "m-card-1",
    space_id: "s",
    conversation_id: "c-general",
    seq: 4,
    author_member_id: "m-lin",
    author_display_name: "Lin",
    author_kind: "agent",
    kind: "action",
    body_markdown: "",
    action_kind: "action_card",
    payload_json: JSON.stringify(payload),
    created_at: "2026-11-22T10:00:00Z",
  };
}

describe("ActionCard", () => {
  it("renders a pending agent.create card, opens review modal on click, and dismisses cleanly", async () => {
    const onReview = vi.fn();
    const dismissSpy = vi.spyOn(api, "dismissActionCard").mockResolvedValue({
      action_card: { version: 1, card_type: "agent.create", status: "dismissed", title: "Create agent" },
    });
    const qc = new QueryClient();
    const msg = makeCardMsg({
      version: 1,
      card_type: "agent.create",
      status: "pending",
      title: "Create agent",
      params: {
        display_name: "XiaoZhu",
        role_text: "Code reviewer",
        driver_kind: "acp",
        driver_config: { provider: "pi" },
      },
    });

    render(
      <QueryClientProvider client={qc}>
        <ActionCard spaceSlug="studio" message={msg} canGovern={true} onReviewAgentProposal={onReview} />
      </QueryClientProvider>,
    );

    expect(screen.getByText("XiaoZhu")).toBeTruthy();
    expect(screen.getByText("Code reviewer")).toBeTruthy();
    expect(screen.getByText("ACP · pi")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Review & create" }));
    expect(onReview).toHaveBeenCalledWith({
      message_id: "m-card-1",
      conversation_id: "c-general",
      proposed_by_name: "Lin",
      params: {
        display_name: "XiaoZhu",
        role_text: "Code reviewer",
        driver_kind: "acp",
        driver_config: { provider: "pi" },
      },
    });

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(dismissSpy).toHaveBeenCalledWith("studio", "m-card-1"));
  });

  it("renders executed state with clickable created agent link and generic fallback for unknown card types", () => {
    const onInspect = vi.fn();
    const qc = new QueryClient();
    const executedMsg = makeCardMsg({
      version: 1,
      card_type: "agent.create",
      status: "executed",
      title: "Create agent",
      params: { display_name: "XiaoZhu" },
      result: { agent_id: "m-xz", display_name: "XiaoZhu" },
      executed_by_name: "Owner",
    });

    const { rerender } = render(
      <QueryClientProvider client={qc}>
        <ActionCard spaceSlug="studio" message={executedMsg} canGovern={true} onInspectAgent={onInspect} />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole("button", { name: "@XiaoZhu" }));
    expect(onInspect).toHaveBeenCalledWith("m-xz");

    const futureMsg = makeCardMsg({
      version: 1,
      card_type: "channel.archive",
      status: "pending",
      title: "Archive #old",
      description: "Make channel read-only",
    });
    rerender(
      <QueryClientProvider client={qc}>
        <ActionCard spaceSlug="studio" message={futureMsg} canGovern={true} />
      </QueryClientProvider>,
    );
    expect(screen.getByText("Archive #old")).toBeTruthy();
    expect(screen.getByText("Make channel read-only")).toBeTruthy();
    expect(parseActionCard("not-json")).toBeNull();
  });
});
