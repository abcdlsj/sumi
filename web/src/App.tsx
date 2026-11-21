import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./api";
import { Onboarding } from "./components/Onboarding";
import { WorkbenchSkeleton } from "./components/Skeletons";
import { ErrorBoundary, ErrorState } from "./components/ui";
import { Workbench } from "./components/Workbench";

type Setup = { slug: string; step: "computer" | "agent" } | null;

export function App() {
  const qc = useQueryClient();
  const [selectedSlug, setSelectedSlug] = useState<string>("");
  // Post-signup steps that run before the Workbench opens.
  const [setup, setSetup] = useState<Setup>(null);
  const [openPeer, setOpenPeer] = useState<string>("");

  const meQ = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: (count, err) => !(err instanceof ApiError && err.status === 401) && count < 2,
  });

  if (meQ.isLoading) return <WorkbenchSkeleton />;

  const isUnauth = meQ.error instanceof ApiError && meQ.error.status === 401;
  if (meQ.isError && !isUnauth) {
    return (
      <div className="h-full bg-bg flex items-center justify-center">
        <ErrorState
          title="Couldn't reach Sumi server"
          message={meQ.error instanceof Error ? meQ.error.message : "Check that the server is running and try again."}
          onRetry={() => void meQ.refetch()}
        />
      </div>
    );
  }

  const user = meQ.isSuccess ? meQ.data.user : null;
  const spaces = meQ.isSuccess ? meQ.data.spaces : [];
  const activeSlug = selectedSlug || spaces[0]?.slug || "";

  function logout() {
    void api.logout().then(() => {
      setSelectedSlug("");
      setSetup(null);
      qc.clear();
      void qc.invalidateQueries({ queryKey: ["me"] });
    });
  }

  if (!user || !activeSlug || setup) {
    const step = !user ? "account" : setup ? setup.step : "space";
    return (
      <ErrorBoundary label="onboarding">
        <Onboarding
          step={step}
          user={user}
          spaceSlug={setup?.slug ?? activeSlug}
          onAuthenticated={() => void qc.invalidateQueries({ queryKey: ["me"] })}
          onSpaceCreated={(slug) => {
            setSelectedSlug(slug);
            setSetup({ slug, step: "computer" });
            void qc.invalidateQueries({ queryKey: ["me"] });
          }}
          onComputerDone={() => setSetup((s) => (s ? { ...s, step: "agent" } : s))}
          onFinish={(agentID) => {
            setOpenPeer(agentID ?? "");
            setSetup(null);
          }}
          onLogout={logout}
        />
      </ErrorBoundary>
    );
  }

  return (
    <ErrorBoundary resetKey={activeSlug} label="workspace">
      <div className="h-full">
        <Workbench
          key={activeSlug}
          spaceSlug={activeSlug}
          spaces={spaces}
          onSwitchSpace={setSelectedSlug}
          onSpaceCreated={(slug) => {
            setSelectedSlug(slug);
            setSetup({ slug, step: "computer" });
            void qc.invalidateQueries({ queryKey: ["me"] });
          }}
          initialPeerID={openPeer}
          onLogout={logout}
        />
      </div>
    </ErrorBoundary>
  );
}
