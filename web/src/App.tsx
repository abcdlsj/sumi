import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./api";
import { Onboarding } from "./components/Onboarding";
import { WorkbenchSkeleton } from "./components/Skeletons";
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
    );
  }

  return (
    <div className="h-full animate-fade-in">
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
  );
}
