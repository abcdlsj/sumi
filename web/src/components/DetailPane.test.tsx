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
