import { CircleCheck, CircleAlert, LoaderCircle, WifiOff, X } from "lucide-react";
import { useEffect, useState } from "react";

import { dismiss, useToasts } from "../feedback";
import type { SpaceConnection } from "../hooks/useSpaceEvents";

export function Toaster() {
  const toasts = useToasts();
  return (
    <div className="toaster" aria-live="polite">
      {toasts.map((toast) => (
        <div key={toast.id} className={`toast toast--${toast.tone}`} role={toast.tone === "error" ? "alert" : undefined}>
          {toast.tone === "error" ? <CircleAlert aria-hidden="true" /> : <CircleCheck aria-hidden="true" />}
          <span>{toast.text}</span>
          <button type="button" aria-label="Dismiss" onClick={() => dismiss(toast.id)}><X aria-hidden="true" /></button>
        </div>
      ))}
    </div>
  );
}

/** Says so when live updates stop; brief blips under a few seconds stay quiet. */
export function ConnectionBanner({ connection, reconnect }: { connection: SpaceConnection; reconnect: () => void }) {
  const down = connection === "reconnecting" || connection === "offline";
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const timer = window.setTimeout(() => setVisible(down), down ? 2500 : 0);
    return () => window.clearTimeout(timer);
  }, [down]);
  if (!visible || !down) return null;
  return (
    <div className="connection-banner" role="status">
      {connection === "offline" ? <WifiOff aria-hidden="true" /> : <LoaderCircle className="spin" aria-hidden="true" />}
      <span>
        <strong>{connection === "offline" ? "You are offline." : "Reconnecting…"}</strong>{" "}
        {connection === "offline" ? "Sumi catches up as soon as the network is back." : "New messages will appear once the connection returns."}
      </span>
      {connection === "reconnecting" ? <button type="button" onClick={reconnect}>Retry now</button> : null}
    </div>
  );
}
