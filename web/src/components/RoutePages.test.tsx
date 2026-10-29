import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory } from "@tanstack/react-router";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { createAppRouter } from "../router";

afterEach(cleanup);

describe("Not found page", () => {
  it("explains an unknown address and offers a way back to sign in", async () => {
    renderRoute("/no/such/page");
    expect(await screen.findByRole("heading", { name: "This page does not exist" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Go to sign in" })).toHaveAttribute("href", "/login");
  });
});

function renderRoute(path: string) {
  const router = createAppRouter(createMemoryHistory({ initialEntries: [path] }));
  render(<QueryClientProvider client={new QueryClient()}><RouterProvider router={router} /></QueryClientProvider>);
}
