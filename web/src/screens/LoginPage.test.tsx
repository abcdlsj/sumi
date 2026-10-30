import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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

describe("LoginPage", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("enters the Member's Space when the identity already belongs to one", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path === "/api/v1/auth/login" && init?.method === "POST") return json({ user });
      if (path === "/api/v1/spaces" && !init?.method) return json([space]);
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

    const router = await signIn(fetchMock);

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/s/sumi-lab/channels/general");
    });
  });

  it("offers Space creation when the identity belongs to no Space", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path === "/api/v1/auth/login" && init?.method === "POST") return json({ user });
      if (path === "/api/v1/spaces" && !init?.method) return json([]);
      if (path === "/api/v1/auth/me") return json(user);
      throw new Error(`Unexpected request: ${path}`);
    });

    const router = await signIn(fetchMock);

    await waitFor(() => {
      expect(router.state.location.pathname).toBe("/spaces/new");
    });
    expect(screen.getByRole("heading", { name: "Create your Space." })).toBeVisible();
  });
});

async function signIn(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal("fetch", fetchMock);
  const router = createAppRouter(createMemoryHistory({ initialEntries: ["/login"] }));
  await router.load();
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  fireEvent.change(await screen.findByLabelText("Email"), { target: { value: user.email } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "correct-horse-battery" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  return router;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
