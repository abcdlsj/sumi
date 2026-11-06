import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAppRouter } from "./router";

const tasksChunk = vi.hoisted(() => ({ loads: 0 }));

vi.mock("./screens/TasksPage", () => {
  tasksChunk.loads += 1;
  return { TasksPage: () => <h1>Tasks</h1>, TaskDetailPage: () => null };
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Lazy Space pages", () => {
  it("downloads a page's code when its link is hovered, before any click", async () => {
    const spaceId = "019c0000-0000-7000-8000-000000000501";
    const ownerId = "019c0000-0000-7000-8000-000000000502";
    const generalId = "019c0000-0000-7000-8000-000000000503";
    const owner = { id: ownerId, kind: "human", display_name: "Ada", access_level: "owner", permissions: [] };
    vi.stubGlobal("EventSource", class { addEventListener() {} removeEventListener() {} close() {} });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/spaces/by-slug/")) return json({ id: spaceId, name: "Sumi Lab", slug: "sumi-lab", accent: "#F0602F", owner_member_id: ownerId, current_member_id: ownerId, general_channel_id: generalId });
      if (path === "/api/v1/auth/me") return json({ id: "user", display_name: "Ada", email: "ada@example.test" });
      if (path.endsWith("/channels")) return json({ can_create: true, channels: [{ id: generalId, space_id: spaceId, kind: "public", slug: "general", created_by_member_id: ownerId, joined: true }] });
      if (path.endsWith(`/channels/${generalId}/members`)) return json({ members: [owner], can_manage: true });
      if (path.endsWith(`/channels/${generalId}/messages`)) {
        return json({ channel_id: generalId, snapshot_channel_seq: 0, messages: [], has_more_before: false, has_more_after: false });
      }
      if (path.endsWith("/members")) return json([owner]);
      return json([]);
    }));
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/s/sumi-lab/channels/general"] }));
    render(<QueryClientProvider client={new QueryClient()}><RouterProvider router={router} /></QueryClientProvider>);

    expect(await screen.findByRole("heading", { name: "#general starts here." })).toBeVisible();
    expect(tasksChunk.loads).toBe(0);

    const tools = screen.getByRole("navigation", { name: "Space tools" });
    fireEvent.mouseEnter(within(tools).getByRole("link", { name: "Tasks" }));
    await waitFor(() => expect(tasksChunk.loads).toBe(1));
    expect(screen.queryByRole("heading", { name: "Tasks" })).not.toBeInTheDocument();
  });
});

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}
