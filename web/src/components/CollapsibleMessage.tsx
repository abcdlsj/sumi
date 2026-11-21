import { useCallback, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";

const COLLAPSE_HEIGHT_PX = 320;
const CACHE_LIMIT = 200;

const expandedIDs = new Map<string, true>();
const callbacks = new Map<Element, (height: number) => void>();
let sharedObserver: ResizeObserver | null = null;

function rememberExpanded(id: string) {
  expandedIDs.delete(id);
  expandedIDs.set(id, true);
  while (expandedIDs.size > CACHE_LIMIT) {
    const oldest = expandedIDs.keys().next().value;
    if (!oldest) break;
    expandedIDs.delete(oldest);
  }
}

function observeHeight(el: HTMLElement, onMeasure: (h: number) => void) {
  if (typeof ResizeObserver === "undefined") {
    onMeasure(el.scrollHeight);
    return () => {};
  }
  if (!sharedObserver) {
    sharedObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const box = entry.borderBoxSize?.[0];
        const h = box ? box.blockSize : entry.contentRect.height;
        callbacks.get(entry.target)?.(h);
      }
    });
  }
  callbacks.set(el, onMeasure);
  sharedObserver.observe(el);
  return () => {
    sharedObserver?.unobserve(el);
    callbacks.delete(el);
    if (callbacks.size === 0) {
      sharedObserver?.disconnect();
      sharedObserver = null;
    }
  };
}

/**
 * Collapses long message bodies past 320px with a CSS mask fade so row hover highlights
 * show cleanly through the fold. Remembers expanded messages across remounts.
 */
export function CollapsibleMessage({
  messageID,
  disabled = false,
  children,
}: {
  messageID: string;
  disabled?: boolean;
  children: ReactNode;
}) {
  const measureRef = useRef<HTMLDivElement | null>(null);
  const [overflows, setOverflows] = useState(false);
  const [expanded, setExpanded] = useState(() => expandedIDs.has(messageID));

  const onMeasure = useCallback((height: number) => {
    setOverflows(height > COLLAPSE_HEIGHT_PX);
  }, []);

  useLayoutEffect(() => {
    if (disabled) return;
    const el = measureRef.current;
    if (!el) return;
    if (el.scrollHeight > 0) {
      setOverflows(el.scrollHeight > COLLAPSE_HEIGHT_PX);
    }
    return observeHeight(el, onMeasure);
  }, [disabled, onMeasure]);

  if (disabled) return <>{children}</>;

  const collapsed = overflows && !expanded;

  return (
    <div data-collapsible={overflows ? "true" : "false"}>
      <div
        className={collapsed ? "relative overflow-clip mask-fade-b" : ""}
        style={collapsed ? { maxHeight: COLLAPSE_HEIGHT_PX } : undefined}
      >
        <div ref={measureRef}>{children}</div>
      </div>
      {overflows && (
        <button
          type="button"
          onClick={() => {
            const next = !expanded;
            if (next) rememberExpanded(messageID);
            else expandedIDs.delete(messageID);
            setExpanded(next);
          }}
          className="mt-1.5 h-6 px-2 -ml-2 rounded-[5px] text-[12.5px] font-medium text-accent-text hover:bg-accent-soft inline-flex items-center gap-1 transition-colors"
        >
          {expanded ? (
            <>
              <ChevronUp className="size-3.5" />
              Show less
            </>
          ) : (
            <>
              <ChevronDown className="size-3.5" />
              Show more
            </>
          )}
        </button>
      )}
    </div>
  );
}
