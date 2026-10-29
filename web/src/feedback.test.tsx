import { QueryClient, QueryClientProvider, useMutation } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { Toaster } from "./components/Feedback";
import { feedbackMutationCache } from "./feedback";

afterEach(cleanup);

function Action({ fail }: { fail: boolean }) {
  const action = useMutation({
    mutationFn: async () => {
      if (fail) throw new Error("Server unreachable.");
      return { seq: 4 };
    },
    meta: { success: (data) => `Task !${(data as { seq: number }).seq} created.`, error: "Could not create the Task." },
  });
  return <button type="button" onClick={() => action.mutate()}>Run</button>;
}

function renderAction(fail: boolean) {
  const client = new QueryClient({ mutationCache: feedbackMutationCache() });
  render(<QueryClientProvider client={client}><Action fail={fail} /><Toaster /></QueryClientProvider>);
}

describe("mutation feedback", () => {
  it("confirms a success with the line the mutation names", async () => {
    renderAction(false);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Run" })));
    expect(await screen.findByText("Task !4 created.")).toBeVisible();
  });

  it("explains a failure with the Server's reason", async () => {
    renderAction(true);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Run" })));
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not create the Task. Server unreachable.");
  });
});
