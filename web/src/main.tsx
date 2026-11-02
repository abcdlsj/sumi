import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "@fontsource-variable/noto-sans-sc";
import "./styles.css";
import "./components/channel/channel.css";
import "./styles/touch.css";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { ApiRequestError } from "./api/client";
import { feedbackMutationCache } from "./feedback";
import { router } from "./router";

const queryClient = new QueryClient({
  mutationCache: feedbackMutationCache(),
  defaultOptions: {
    queries: {
      // A 4xx answer (signed out, forbidden, missing) will not change on retry; retrying a 401
      // held the skeleton for about 7 s before the redirect to sign-in.
      retry: (failureCount, error) =>
        !(error instanceof ApiRequestError && error.status >= 400 && error.status < 500) && failureCount < 3,
    },
  },
});

const root = document.getElementById("root");
if (!root) {
  throw new Error("Sumi root element is missing");
}

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
