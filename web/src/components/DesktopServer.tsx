import { useEffect, useState, type FormEvent } from "react";
import { Globe, HardDrive } from "lucide-react";
import {
  fetchDesktopServer,
  formatServerLabel,
  getDesktopBridge,
  switchDesktopServer,
  type DesktopServerState,
} from "../shell";
import { Button, Field, Modal, Segmented } from "./ui";

export function DesktopServerTrigger({ variant }: { variant: "rail" | "pill" }) {
  const bridge = getDesktopBridge();
  const [state, setState] = useState<DesktopServerState | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!bridge) return;
    let active = true;
    void fetchDesktopServer(bridge)
      .then((s) => {
        if (active) setState(s);
      })
      .catch(() => {
        // Control server is optional if running outside the desktop shell.
      });
    return () => {
      active = false;
    };
  }, [bridge?.controlUrl, bridge?.token]);

  if (!bridge) return null;

  const isLocal = state ? state.is_local : window.location.hostname === "127.0.0.1" || window.location.hostname === "localhost";
  const currentUrl = state?.server_url ?? window.location.origin;
  const label = formatServerLabel(currentUrl, isLocal);

  return (
    <>
      {variant === "rail" ? (
        <button
          type="button"
          onClick={() => setOpen(true)}
          title={`Server: ${label} · Switch`}
          aria-label={`Server: ${label}`}
          className="relative mt-1 size-9 rounded-[10px] flex items-center justify-center text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong transition-colors"
        >
          {isLocal ? <HardDrive className="size-[16px]" /> : <Globe className="size-[16px]" />}
          {!isLocal && <span className="absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-[var(--nav-rail)] bg-accent" />}
        </button>
      ) : (
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label={`Server: ${label}`}
          className="inline-flex items-center gap-1.5 px-2.5 h-7 rounded-[7px] text-[12.5px] text-nav-fg-3 hover:text-nav-fg-strong hover:bg-nav-hover transition-colors"
        >
          {isLocal ? <HardDrive className="size-3.5" /> : <Globe className="size-3.5 text-accent" />}
          <span className="font-mono truncate max-w-[220px]">{label}</span>
        </button>
      )}
      <DesktopServerDialog
        open={open}
        onClose={() => setOpen(false)}
        currentUrl={currentUrl}
        defaultLocalUrl={state?.default_local_url ?? "http://127.0.0.1:8087"}
        isLocal={isLocal}
        onSwitch={async (target) => {
          const next = await switchDesktopServer(bridge, target);
          setState(next);
          setOpen(false);
        }}
      />
    </>
  );
}

function DesktopServerDialog({
  open,
  onClose,
  currentUrl,
  defaultLocalUrl,
  isLocal,
  onSwitch,
}: {
  open: boolean;
  onClose: () => void;
  currentUrl: string;
  defaultLocalUrl: string;
  isLocal: boolean;
  onSwitch: (target: string) => Promise<void>;
}) {
  const [mode, setMode] = useState<"local" | "remote">(isLocal ? "local" : "remote");
  const [remoteUrl, setRemoteUrl] = useState(isLocal ? "" : currentUrl);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (open) {
      setMode(isLocal ? "local" : "remote");
      setRemoteUrl(isLocal ? "" : currentUrl);
      setError("");
      setBusy(false);
    }
  }, [open, isLocal, currentUrl]);

  const target = mode === "local" ? defaultLocalUrl : remoteUrl.trim();
  const unchanged = target === currentUrl;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || unchanged) return;
    setBusy(true);
    setError("");
    try {
      await onSwitch(mode === "local" ? "local" : remoteUrl.trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : "Unable to switch server");
      setBusy(false);
    }
  }

  return (
    <Modal open={open} onClose={onClose} label="Server" width={420}>
      <div className="px-6 pt-5 pb-3 border-b border-line">
        <h2 className="text-[16px] font-semibold text-fg">Server</h2>
        <p className="mt-0.5 text-[12.5px] font-mono text-fg-3 truncate">{currentUrl}</p>
      </div>
      <form onSubmit={(e) => void submit(e)} className="px-6 py-5 space-y-4">
        <Segmented
          value={mode}
          onChange={(v) => {
            setMode(v);
            setError("");
          }}
          options={[
            { value: "local", label: "Local" },
            { value: "remote", label: "Remote" },
          ]}
        />
        {mode === "local" ? (
          <p className="text-[13px] text-fg-2">
            Connect to this machine&apos;s built-in server at <span className="font-mono text-fg">{defaultLocalUrl}</span>.
          </p>
        ) : (
          <Field
            label="Server URL"
            autoFocus
            value={remoteUrl}
            onChange={(e) => {
              setRemoteUrl(e.target.value);
              setError("");
            }}
            placeholder="https://sumi.example.com"
            className="font-mono"
            required
          />
        )}
        {error && (
          <p role="alert" className="text-[13px] text-danger">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={unchanged || (mode === "remote" && !remoteUrl.trim())}>
            Switch
          </Button>
        </div>
      </form>
    </Modal>
  );
}
