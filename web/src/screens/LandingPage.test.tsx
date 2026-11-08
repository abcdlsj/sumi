import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { createAppRouter } from "../router";

const user = {
  id: "019c0000-0000-7000-8000-000000000020",
  display_name: "Ada Lovelace",
  email: "ada@example.test",
};

const space = {
  id: "019c0000-0000-7000-8000-000000000010",
  name: "Sumi Lab",
  slug: "sumi-lab",
  accent: "#F0602F",
  owner_member_id: "019c0000-0000-7000-8000-000000000011",
  current_member_id: "019c0000-0000-7000-8000-000000000011",
  general_channel_id: "019c0000-0000-7000-8000-000000000012",
};

const secondSpace = {
  ...space,
  id: "019c0000-0000-7000-8000-000000000030",
  name: "Sumi Build",
  slug: "build",
  accent: "#3C9E8F",
  owner_member_id: "019c0000-0000-7000-8000-000000000031",
  current_member_id: "019c0000-0000-7000-8000-000000000031",
  general_channel_id: "019c0000-0000-7000-8000-000000000032",
};

describe("LandingPage", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("is the registration form when the caller has no Session", async () => {
    const router = await load(fetchWith(signedOut));

    expect(await screen.findByRole("heading", { name: "Join your collaborators." })).toBeVisible();
    expect(router.state.location.pathname).toBe("/");
  });

  it("opens the only Space without asking", async () => {
    const router = await load(fetchWith(() => json([space])));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/s/sumi-lab/channels/general");
    });
  });

  it("asks which Space when the identity belongs to several", async () => {
    const router = await load(fetchWith(() => json([space, secondSpace])));

    expect(await screen.findByRole("heading", { name: "Choose a Space." })).toBeVisible();
    expect(screen.getByRole("link", { name: /Sumi Lab/ })).toHaveAttribute("href", "/s/sumi-lab");
    expect(screen.getByRole("link", { name: /Sumi Build/ })).toHaveAttribute("href", "/s/build");
    expect(router.state.location.pathname).toBe("/spaces");
  });

  it("offers Space creation when the identity belongs to none", async () => {
    const router = await load(fetchWith(() => json([])));

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/spaces/new");
    });
  });
});

async function load(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal("fetch", fetchMock);
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/"] }));
  await router.load();
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

/** `GET /api/v1/spaces` answers from the test; everything else is what an open Space asks for. */
function fetchWith(spaces: () => Response): ReturnType<typeof vi.fn> {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    if (path === "/api/v1/spaces" && !init?.method) return spaces();
    if (path === "/api/v1/auth/me") return json(user);
    if (path.includes("/spaces/by-slug/")) return json(space);
    if (path.endsWith("/channels") && !init?.method) {
      return json({
        can_create: true,
        channels: [
          {
            id: space.general_channel_id,
            space_id: space.id,
            kind: "public",
            slug: "general",
            created_by_member_id: space.owner_member_id,
            joined: true,
          },
        ],
      });
    }
    if (path.endsWith("/dms") && !init?.method) return json([]);
    if (path.endsWith("/members") && !init?.method) {
      return json([{
        id: space.owner_member_id,
        kind: "human",
        display_name: user.display_name,
        access_level: "owner",
        permissions: [],
      }]);
    }
    if (path.endsWith("/computers") && !init?.method) return json([]);
    if (path.endsWith("/agents") && !init?.method) return json([]);
    if (path.endsWith(`/channels/${space.general_channel_id}/messages`) && !init?.method) {
      return json({ messages: [], has_more: false });
    }
    throw new Error(`Unexpected request: ${path}`);
  });
}

function signedOut(): Response {
  return json({ error: { code: "unauthorized", message: "Sign in first" } }, 401);
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
