import { useCallback, useEffect, useRef, useState } from "react";

const KEY = "sumi.layout";
const RAIL = 56;
export const SIDEBAR_MIN = 200;
export const SIDEBAR_MAX = 420;
const SIDEBAR_DEFAULT = 260;
export const DETAIL_MIN = 340;
const DETAIL_CAP = 880;
const MAIN_MIN = 440;

type Saved = { sidebar?: number; detail?: number };

function load(): Saved {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? "{}") as Saved;
  } catch {
    return {};
  }
}

function save(v: Saved) {
  try {
    localStorage.setItem(KEY, JSON.stringify(v));
  } catch {
    // Storage may be unavailable (private mode); widths then reset per session.
  }
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

function useViewportWidth() {
  const [w, setW] = useState(() => window.innerWidth);
  useEffect(() => {
    const on = () => setW(window.innerWidth);
    window.addEventListener("resize", on);
    return () => window.removeEventListener("resize", on);
  }, []);
  return w;
}

/**
 * Sidebar and detail-pane widths. The detail pane opens at its maximum (whatever leaves the
 * main pane MAIN_MIN wide, capped at DETAIL_CAP) until the user drags it; the dragged width is remembered.
 */
export function usePaneWidths() {
  const vw = useViewportWidth();
  const [saved, setSaved] = useState<Saved>(load);
  const sidebar = clamp(saved.sidebar ?? SIDEBAR_DEFAULT, SIDEBAR_MIN, SIDEBAR_MAX);
  const detailMax = Math.max(DETAIL_MIN, Math.min(DETAIL_CAP, vw - RAIL - sidebar - MAIN_MIN));
  const detail = clamp(saved.detail ?? detailMax, DETAIL_MIN, detailMax);
  const update = useCallback((patch: Saved) => {
    setSaved((prev) => {
      const next = { ...prev, ...patch };
      save(next);
      return next;
    });
  }, []);
  return {
    sidebar,
    detail,
    detailMax,
    setSidebar: (w: number) => update({ sidebar: clamp(Math.round(w), SIDEBAR_MIN, SIDEBAR_MAX) }),
    setDetail: (w: number) => update({ detail: clamp(Math.round(w), DETAIL_MIN, detailMax) }),
    resetSidebar: () => update({ sidebar: undefined }),
    resetDetail: () => update({ detail: undefined }),
  };
}

/** Vertical drag handle; `edge` is the side of the pane it sits on. Double-click resets. */
export function ResizeHandle({
  label,
  edge,
  value,
  min,
  max,
  onResize,
  onReset,
}: {
  label: string;
  edge: "left" | "right";
  value: number;
  min: number;
  max: number;
  onResize: (w: number) => void;
  onReset: () => void;
}) {
  const [dragging, setDragging] = useState(false);
  const start = useRef<{ x: number; w: number } | null>(null);
  const sign = edge === "right" ? 1 : -1;
  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={value}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      data-dragging={dragging}
      className="resize-handle"
      style={edge === "right" ? { right: -5 } : { left: -5 }}
      onPointerDown={(e) => {
        e.preventDefault();
        (e.target as HTMLElement).setPointerCapture(e.pointerId);
        start.current = { x: e.clientX, w: value };
        setDragging(true);
        document.body.style.cursor = "col-resize";
        document.body.style.userSelect = "none";
      }}
      onPointerMove={(e) => {
        if (!start.current) return;
        onResize(start.current.w + sign * (e.clientX - start.current.x));
      }}
      onPointerUp={() => {
        start.current = null;
        setDragging(false);
        document.body.style.cursor = "";
        document.body.style.userSelect = "";
      }}
      onDoubleClick={onReset}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft") onResize(value - 16 * sign);
        if (e.key === "ArrowRight") onResize(value + 16 * sign);
      }}
    />
  );
}
