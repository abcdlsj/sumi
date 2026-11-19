import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "./api";
import { AuthGate } from "./components/AuthGate";
import { Workbench } from "./components/Workbench";

export function App() {
  const qc = useQueryClient();
  const [selectedSlug, setSelectedSlug] = useState<string>("");

  const meQ = useQuery({
    queryKey: ["me"],
    queryFn: () => api.me(),
    retry: (_, err) => !(err instanceof ApiError && err.status === 401),
  });

  if (meQ.isLoading) {
    return (
      <div className="h-full bg-[#F4F2EC] flex items-center justify-center text-[13px] text-[#6E6B64]">
        Loading Sumi…
      </div>
    );
  }

  const user = meQ.isSuccess ? meQ.data.user : null;
  const spaces = meQ.isSuccess ? meQ.data.spaces : [];
  const activeSlug = selectedSlug || spaces[0]?.slug || "";

  if (!user || !activeSlug) {
    return (
      <AuthGate
        user={user}
        spaces={spaces}
        onAuthenticated={() => void qc.invalidateQueries({ queryKey: ["me"] })}
        onSpaceReady={(slug) => {
          setSelectedSlug(slug);
          void qc.invalidateQueries({ queryKey: ["me"] });
        }}
      />
    );
  }

  return (
    <Workbench
      spaceSlug={activeSlug}
      onLogout={() => {
        void api.logout().then(() => {
          setSelectedSlug("");
          qc.clear();
          void qc.invalidateQueries({ queryKey: ["me"] });
        });
      }}
    />
  );
}
