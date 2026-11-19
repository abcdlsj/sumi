import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData } from "../api";
import { DetailPane } from "./DetailPane";

const boot = { members: [], tasks: [], computers: [], conversations: [], inbox: [], current_member: { access_level: "owner" } } as unknown as BootstrapData;

afterEach(() => vi.restoreAllMocks());

describe("ThreadDetail", () => {
  it("starts from the server following state and toggles it", async () => {
    let following = false;
    vi.spyOn(api, "listMessages").mockImplementation(async () => ({ messages: [], read_seq: 0, following }));
    const setFollowing = vi.spyOn(api, "setFollowing").mockImplementation(async (_s, _c, next) => {
      following = next;
      return { ok: true };
    });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <DetailPane
          spaceSlug="s"
          boot={boot}
          target={{ kind: "thread", threadConvID: "th-1", rootMessageID: "m1", parentSlug: "general" }}
          onClose={() => {}}
          onSelectTarget={() => {}}
        />
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Follow" }));
    await waitFor(() => expect(setFollowing).toHaveBeenCalledWith("s", "th-1", true));
    expect(await screen.findByRole("button", { name: "Following" })).toBeTruthy();
  });
});

describe("AgentDetail activity", () => {
  function renderAgent(activity: { tool?: string; stalled_seconds?: number }) {
    const member = {
      id: "a1",
      kind: "agent",
      display_name: "Lin",
      agent: { role_text: "", driver_kind: "builtin", runtime_state: "working", activity },
    };
    const data = { ...boot, members: [member] } as unknown as BootstrapData;
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <DetailPane spaceSlug="s" boot={data} target={{ kind: "agent", memberID: "a1" }} onClose={() => {}} onSelectTarget={() => {}} />
      </QueryClientProvider>,
    );
  }

  it("shows the running tool", () => {
    renderAgent({ tool: "execute" });
    expect(screen.getByText("Running execute")).toBeTruthy();
  });

  it("warns when a tool has been silent", () => {
    renderAgent({ tool: "execute", stalled_seconds: 720 });
    expect(screen.getByText("execute: no output for 12 min")).toBeTruthy();
  });
});
