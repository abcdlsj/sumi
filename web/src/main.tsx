import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import { ToastProvider } from "./components/ui";
import { capturePairingFromURL } from "./pairing";
import { applyTypographyPreferences } from "./preferences";
import { captureShellFromURL, installContextMenuGuard, installExternalLinkHandler } from "./shell";
import { applyTheme } from "./theme";
import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: false,
    },
  },
});

captureShellFromURL();
capturePairingFromURL();
applyTheme();
applyTypographyPreferences();
installContextMenuGuard();
installExternalLinkHandler();

// Gate focus rings by input modality: pointer clicks stay quiet, keyboard navigation stays crisp.
window.addEventListener("pointerdown", () => {
  document.documentElement.dataset.inputModality = "pointer";
}, { capture: true, passive: true });
window.addEventListener("keydown", (e) => {
  if (e.key === "Tab" || e.key.startsWith("Arrow") || e.key === "Enter" || e.key === "Escape" || e.key === " ") {
    document.documentElement.dataset.inputModality = "keyboard";
  }
}, { capture: true });

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <App />
      </ToastProvider>
    </QueryClientProvider>
  </StrictMode>,
);
