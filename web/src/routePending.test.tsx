import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAppRouter } from "./router";

// Holds the Inbox chunk "downloading" until the test releases it.
const chunk = vi.hoisted(() => {
  let release = () => {};
  const loaded = new Promise<void>((resolve) => { release = resolve; });
  return { loaded, release: () => release() };
});

vi.mock("./screens/InboxPage", async () => {
  await chunk.loaded;
  return { InboxPage: () => <h1>Inbox</h1> };
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("Lazy Space pages", () => {
  it("shows the page skeleton inside the Space Shell until the page code arrives", async () => {
    const spaceId = "019c0000-0000-7000-8000-000000000401";
    const ownerId = "019c0000-0000-7000-8000-000000000402";
    const generalId = "019c0000-0000-7000-8000-000000000403";
    const owner = { id: ownerId, kind: "human", display_name: "Ada", access_level: "owner", permissions: [] };
    vi.stubGlobal("EventSource", class { addEventListener() {} removeEventListener() {} close() {} });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.includes("/spaces/by-slug/")) return json({ id: spaceId, name: "Sumi Lab", slug: "sumi-lab", accent: "#F0602F", owner_member_id: ownerId, current_member_id: ownerId, general_channel_id: generalId });
      if (path === "/api/v1/auth/me") return json({ id: "user", display_name: "Ada", email: "ada@example.test" });
      if (path.endsWith("/channels")) return json({ can_create: true, channels: [{ id: generalId, space_id: spaceId, kind: "public", slug: "general", created_by_member_id: ownerId, joined: true }] });
      if (path.endsWith("/members")) return json([owner]);
      return json([]);
    }));
    const router = createAppRouter(createMemoryHistory({ initialEntries: ["/s/sumi-lab/inbox"] }));
    render(<QueryClientProvider client={new QueryClient()}><RouterProvider router={router} /></QueryClientProvider>);

    expect(await screen.findByRole("status", { name: "Opening Inbox" })).toBeVisible();
    expect(screen.getByRole("complementary", { name: "Space navigation" })).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Inbox" })).not.toBeInTheDocument();

    await act(async () => chunk.release());
    expect(await screen.findByRole("heading", { name: "Inbox" })).toBeVisible();
    expect(screen.queryByRole("status", { name: "Opening Inbox" })).not.toBeInTheDocument();
  });
});

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
}
