import { useCallback, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import { useLongMessagesPref } from "../preferences";

const COLLAPSE_HEIGHT_PX = 320;
const CACHE_LIMIT = 200;

interface OverrideEntry {
  version: number;
  expanded: boolean;
}

const messageOverrides = new Map<string, OverrideEntry>();
const callbacks = new Map<Element, (height: number) => void>();
let sharedObserver: ResizeObserver | null = null;

function rememberOverride(id: string, entry: OverrideEntry) {
  messageOverrides.delete(id);
  messageOverrides.set(id, entry);
  while (messageOverrides.size > CACHE_LIMIT) {
    const oldest = messageOverrides.keys().next().value;
    if (!oldest) break;
    messageOverrides.delete(oldest);
  }
}

export function clearCollapsibleOverridesForTests() {
  messageOverrides.clear();
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
 * Allows tall message bodies (past 320px) to fold with a CSS mask fade so row hover
 * highlights show cleanly through the fold. Defaults to the user's General preference
 * (expanded by default) and remembers per-message toggles across remounts.
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
  const [longMessagesPref, , prefVersion] = useLongMessagesPref();
  const defaultExpanded = longMessagesPref === "expanded";
  const measureRef = useRef<HTMLDivElement | null>(null);
  const [overflows, setOverflows] = useState(false);
  const [override, setOverride] = useState<OverrideEntry | undefined>(() => {
    const saved = messageOverrides.get(messageID);
    return saved && saved.version === prefVersion ? saved : undefined;
  });

  const activeOverride = override && override.version === prefVersion ? override.expanded : undefined;
  const expanded = activeOverride ?? defaultExpanded;

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
    <div data-collapsible={overflows ? "true" : "false"} data-collapsed={collapsed ? "true" : "false"}>
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
            if (next === defaultExpanded) {
              messageOverrides.delete(messageID);
              setOverride(undefined);
            } else {
              const entry: OverrideEntry = { version: prefVersion, expanded: next };
              rememberOverride(messageID, entry);
              setOverride(entry);
            }
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
