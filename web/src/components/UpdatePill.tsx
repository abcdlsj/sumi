import { useCallback, useEffect, useRef, useState } from "react";
import {
  applyDesktopUpdate,
  commitDesktopUpdateRestart,
  fetchDesktopUpdateStatus,
  getDesktopBridge,
  openDesktopURL,
  type DesktopUpdateStatus,
} from "../shell";
import { updateFlushRegistry, type FlushRegistry } from "../updateHandoff";

const SMART_AUTO_RESTART_WINDOW_MS = 8_000;
const RECENT_INPUT_GUARD_MS = 2_000;
const IDLE_POLL_MS = 60_000;
const ACTIVE_POLL_MS = 250;

const INITIAL_STATUS: DesktopUpdateStatus = {
  current: "",
  available: false,
  state: "idle",
  progress: 0,
  can_apply: false,
};

interface UseAutoUpdateOptions {
  registry?: FlushRegistry;
  captureRootState: () => Record<string, unknown>;
  onToast?: (message: string) => void;
}

export function useAutoUpdate({
  registry = updateFlushRegistry,
  captureRootState,
  onToast,
}: UseAutoUpdateOptions) {
  const bridge = getDesktopBridge();
  const [status, setStatus] = useState<DesktopUpdateStatus>(INITIAL_STATUS);
  const [flushing, setFlushing] = useState(false);

  const clickedAtRef = useRef<number | null>(null);
  const lastInputAtRef = useRef<number>(0);
  const committingRef = useRef(false);
  const lastErrRef = useRef<string | undefined>(undefined);
  const captureRootRef = useRef(captureRootState);
  captureRootRef.current = captureRootState;
  const onToastRef = useRef(onToast);
  onToastRef.current = onToast;

  // Track recent keystrokes so a slow download never interrupts active typing.
  useEffect(() => {
    if (!bridge) return;
    const markInput = () => {
      lastInputAtRef.current = Date.now();
    };
    window.addEventListener("keydown", markInput, true);
    return () => window.removeEventListener("keydown", markInput, true);
  }, [bridge?.controlUrl, bridge?.token]);

  const commitRestart = useCallback(async () => {
    if (!bridge || committingRef.current) return;
    committingRef.current = true;
    setFlushing(true);
    try {
      await registry.flushAll();
      const snapshot = {
        ...captureRootRef.current(),
        ...registry.captureAll(),
      };
      await commitDesktopUpdateRestart(bridge, snapshot);
    } catch (err) {
      committingRef.current = false;
      setFlushing(false);
      onToastRef.current?.(err instanceof Error ? `Update paused: ${err.message}` : "Update paused to protect unsaved edits");
    }
  }, [bridge, registry]);

  const handleStatus = useCallback(
    (next: DesktopUpdateStatus) => {
      setStatus(next);

      if (next.state === "error" && next.error && next.error !== lastErrRef.current) {
        lastErrRef.current = next.error;
        onToastRef.current?.(`Update failed: ${next.error}`);
      } else if (next.state !== "error") {
        lastErrRef.current = undefined;
      }

      if (next.state === "ready_to_restart" && !committingRef.current) {
        const clickedAt = clickedAtRef.current;
        const withinWindow =
          clickedAt !== null && Date.now() - clickedAt <= SMART_AUTO_RESTART_WINDOW_MS;
        const recentlyTyped = Date.now() - lastInputAtRef.current < RECENT_INPUT_GUARD_MS;
        const blocking = registry.getBlockingReasons();

        if (withinWindow && !recentlyTyped && blocking.length === 0) {
          clickedAtRef.current = null;
          void commitRestart();
        }
      }
    },
    [commitRestart, registry],
  );

  // Poll the local desktop control bridge (no-op outside Sumi desktop shell).
  useEffect(() => {
    if (!bridge) return;
    let active = true;
    let timer: number | null = null;

    const schedule = (delay: number) => {
      if (!active) return;
      timer = window.setTimeout(() => void poll(), delay);
    };

    const poll = async () => {
      let nextDelay = IDLE_POLL_MS;
      try {
        const next = await fetchDesktopUpdateStatus(bridge);
        if (!active) return;
        handleStatus(next);
        if (next.state === "downloading" || next.state === "verifying" || next.state === "checking") {
          nextDelay = ACTIVE_POLL_MS;
        }
      } catch {
        // Control bridge is optional; stay quiet on transient errors.
      }
      schedule(nextDelay);
    };

    void poll();
    return () => {
      active = false;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [bridge?.controlUrl, bridge?.token, handleStatus]);

  const triggerUpdate = useCallback(async () => {
    if (!bridge) return;
    if (
      flushing ||
      status.state === "downloading" ||
      status.state === "verifying" ||
      status.state === "installing"
    ) {
      return;
    }

    if (status.state === "ready_to_restart") {
      const blocking = registry.getBlockingReasons();
      if (blocking.length > 0) {
        onToastRef.current?.(`Waiting before restart: ${blocking[0]}`);
        return;
      }
      await commitRestart();
      return;
    }

    if (!status.available) return;

    if (!status.can_apply) {
      if (status.release_url) {
        void openDesktopURL(bridge, status.release_url).catch(() => {});
      }
      return;
    }

    clickedAtRef.current = Date.now();
    try {
      const next = await applyDesktopUpdate(bridge);
      handleStatus(next);
    } catch (err) {
      clickedAtRef.current = null;
      onToastRef.current?.(err instanceof Error ? `Update failed: ${err.message}` : "Update failed");
    }
  }, [bridge, commitRestart, flushing, handleStatus, registry, status]);

  const effectiveStatus: DesktopUpdateStatus = flushing
    ? { ...status, state: "flushing" }
    : status;

  return {
    status: effectiveStatus,
    triggerUpdate,
  };
}

interface UpdatePillProps {
  status: DesktopUpdateStatus;
  onActivate: () => void;
  compact?: boolean;
}

/**
 * Ultra-minimal, zero-interruption desktop update pill.
 * Completely absent outside Sumi desktop shell and whenever idle or checking.
 */
export function UpdatePill({ status, onActivate, compact = false }: UpdatePillProps) {
  if (!getDesktopBridge()) return null;

  const visible =
    status.available ||
    status.state === "downloading" ||
    status.state === "verifying" ||
    status.state === "ready_to_restart" ||
    status.state === "flushing" ||
    status.state === "installing";

  if (!visible) return null;

  const busy =
    status.state === "downloading" ||
    status.state === "verifying" ||
    status.state === "flushing" ||
    status.state === "installing";

  const versionLabel = status.latest ?? "";
  let label: string;
  let title: string;

  switch (status.state) {
    case "downloading": {
      const pct = Math.max(0, Math.min(99, status.progress || 0));
      label = compact ? `${pct}%` : `↓ ${pct}%`;
      title = `Downloading v${versionLabel} (${pct}%)`;
      break;
    }
    case "verifying":
      label = compact ? "99%" : "↓ 99%";
      title = `Verifying signature for v${versionLabel}…`;
      break;
    case "ready_to_restart":
      label = compact ? "↻" : `↻ ${versionLabel}`;
      title = `v${versionLabel} ready — click to save & restart cleanly`;
      break;
    case "flushing":
    case "installing":
      label = "↻ …";
      title = `Saving state & restarting into v${versionLabel}…`;
      break;
    case "error":
      label = compact ? "↑!" : `↑ ${versionLabel} !`;
      title = status.error
        ? `Update failed: ${status.error} — click to retry`
        : "Update failed — click to retry";
      break;
    default:
      label = compact ? "↑" : `↑ ${versionLabel}`;
      title = status.can_apply
        ? `Update to v${versionLabel} — click to download & restart`
        : `v${versionLabel} available — click to open GitHub release`;
      break;
  }

  const toneClass =
    status.state === "ready_to_restart"
      ? "bg-accent text-accent-fg hover:bg-accent-hover font-semibold border border-transparent"
      : status.state === "error"
        ? "bg-nav-fill text-nav-danger border border-nav-danger/40 hover:bg-nav-fill-2"
        : busy
          ? "bg-nav-fill text-nav-fg-2 border border-nav-line opacity-85 cursor-default"
          : "bg-nav-fill text-nav-fg-2 border border-nav-line-strong hover:bg-nav-fill-2 hover:text-nav-fg-strong";

  return (
    <button
      type="button"
      disabled={busy}
      aria-busy={busy || undefined}
      aria-label={title}
      title={title}
      onClick={onActivate}
      style={{ ["--wails-draggable" as string]: "no-drag" }}
      className={`${
        compact ? "size-9 text-[11px] rounded-[5px]" : "h-6 px-2 text-[11.5px] rounded-[4px]"
      } inline-flex items-center justify-center font-mono tabular leading-none shrink-0 transition-colors ${toneClass}`}
    >
      {label}
    </button>
  );
}
