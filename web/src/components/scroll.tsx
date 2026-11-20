import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowDown, Loader2 } from "lucide-react";
import { api, type Message } from "../api";

/** Distance from the end that still counts as reading the newest message. */
const NEAR_END_PX = 80;

function distanceFromEnd(el: HTMLElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight;
}

export interface StickToBottomOptions {
  /** Identity of the stream; switching streams re-places the view. */
  streamKey: string;
  /** Messages currently rendered. */
  count: number;
  /** Element id of the persisted unread divider, used for the first placement only. */
  anchorId?: string;
}

export interface StickToBottom {
  ref: React.RefObject<HTMLDivElement | null>;
  /** Attach to the scroller's `onScroll`. */
  onScroll: () => void;
  /** Messages that arrived while the reader was above the end. */
  newCount: number;
  /** Jump to the end and clear the counter. */
  jumpToEnd: () => void;
}

/**
 * Keeps a message stream glued to its end while the reader stays there. Scrolling up
 * parks the stream and counts later arrivals instead of yanking the view; the first
 * placement of a stream lands on the unread divider when there is one, else the end.
 */
export function useStickToBottom({ streamKey, count, anchorId }: StickToBottomOptions): StickToBottom {
  const ref = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);
  const placed = useRef(false);
  const anchored = useRef(false);
  const seen = useRef(0);
  const lastTop = useRef(0);
  const moved = useRef(false);
  const [newCount, setNewCount] = useState(0);

  /** Records our own scroll positions so they are not read as the reader moving. */
  const setTop = useCallback((el: HTMLElement, top: number) => {
    el.scrollTop = top;
    lastTop.current = el.scrollTop;
  }, []);

  const jumpToEnd = useCallback(() => {
    const el = ref.current;
    if (el) setTop(el, el.scrollHeight);
    pinned.current = true;
    setNewCount(0);
  }, [setTop]);

  useLayoutEffect(() => {
    pinned.current = true;
    placed.current = false;
    anchored.current = false;
    seen.current = 0;
    lastTop.current = 0;
    moved.current = false;
    setNewCount(0);
  }, [streamKey]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || count === 0) return;
    const grew = Math.max(0, count - seen.current);
    seen.current = count;
    const anchor = anchorId ? el.querySelector<HTMLElement>(`[data-unread-anchor="${anchorId}"]`) : null;

    // First placement, and a divider that only shows up once the fresh page lands.
    if (!placed.current || (!anchored.current && anchor && !moved.current)) {
      placed.current = true;
      if (anchor) {
        anchored.current = true;
        setTop(el, Math.max(0, anchor.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 16));
      } else {
        setTop(el, el.scrollHeight);
      }
      pinned.current = distanceFromEnd(el) < NEAR_END_PX;
      return;
    }
    if (!pinned.current) {
      if (grew > 0) setNewCount((n) => n + grew);
      return;
    }
    setTop(el, el.scrollHeight);
  }, [count, anchorId, setTop]);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    if (Math.abs(el.scrollTop - lastTop.current) > 1) moved.current = true;
    const atEnd = distanceFromEnd(el) < NEAR_END_PX;
    pinned.current = atEnd;
    if (atEnd) setNewCount(0);
  }, []);

  // Images and embeds settle after their row rendered; keep the end in view.
  const observer = useRef<ResizeObserver | null>(null);
  const watched = useRef<Element | null>(null);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    if (!observer.current) {
      observer.current = new ResizeObserver(() => {
        const box = ref.current;
        if (!box || !pinned.current) return;
        box.scrollTop = box.scrollHeight;
        lastTop.current = box.scrollTop;
      });
    }
    const content = el.firstElementChild;
    if (content === watched.current) return;
    observer.current.disconnect();
    observer.current.observe(el);
    if (content) observer.current.observe(content);
    watched.current = content;
  }, [streamKey, count]);
  useEffect(() => () => observer.current?.disconnect(), []);

  return { ref, onScroll, newCount, jumpToEnd };
}

/** Floating "N new messages" pill shown while the stream is parked above its end. */
export function NewMessagesPill({ count, onClick }: { count: number; onClick: () => void }) {
  if (count <= 0) return null;
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center">
      <button
        type="button"
        onClick={onClick}
        className="pointer-events-auto h-8 pl-3 pr-2.5 rounded-full bg-accent text-accent-fg shadow-md text-[13px] font-medium inline-flex items-center gap-1.5 animate-rise-in hover:bg-accent-hover transition-colors"
      >
        {count} new {count === 1 ? "message" : "messages"}
        <ArrowDown className="size-4" />
      </button>
    </div>
  );
}

/** Prepends older pages before the live tail while preserving the reader's scroll position. */
export function useOlderMessages({
  spaceSlug,
  convID,
  latest,
  latestHasMore,
  scrollRef,
}: {
  spaceSlug: string;
  convID?: string;
  latest?: Message[];
  latestHasMore?: boolean;
  scrollRef: React.RefObject<HTMLDivElement | null>;
}) {
  const [older, setOlder] = useState<Message[]>([]);
  const [hasOlder, setHasOlder] = useState<boolean | null>(null);
  const [loading, setLoading] = useState(false);
  const pendingHeight = useRef<number | null>(null);

  useEffect(() => {
    setOlder([]);
    setHasOlder(null);
    setLoading(false);
    pendingHeight.current = null;
  }, [convID]);

  useLayoutEffect(() => {
    const el = scrollRef.current;
    if (!el || pendingHeight.current === null) return;
    const delta = el.scrollHeight - pendingHeight.current;
    if (delta > 0) el.scrollTop += delta;
    pendingHeight.current = null;
  }, [older, scrollRef]);

  const messages = useMemo(() => {
    if (!latest) return undefined;
    if (older.length === 0) return latest;
    const seen = new Set(older.map((m) => m.id));
    return [...older, ...latest.filter((m) => !seen.has(m.id))];
  }, [older, latest]);

  const hasMore = hasOlder ?? Boolean(latestHasMore);

  const loadEarlier = useCallback(async () => {
    if (!convID || loading || !hasMore) return;
    const oldest = messages?.[0];
    if (!oldest) return;
    setLoading(true);
    try {
      const page = await api.listMessages(spaceSlug, convID, false, { beforeSeq: oldest.seq });
      if (scrollRef.current) pendingHeight.current = scrollRef.current.scrollHeight;
      setOlder((prev) => {
        const seen = new Set(prev.map((m) => m.id));
        return [...page.messages.filter((m) => !seen.has(m.id)), ...prev];
      });
      setHasOlder(Boolean(page.has_more));
    } finally {
      setLoading(false);
    }
  }, [spaceSlug, convID, loading, hasMore, messages, scrollRef]);

  return { messages, hasMore, loading, loadEarlier };
}

export function LoadEarlierButton({ hasMore, loading, onClick }: { hasMore: boolean; loading: boolean; onClick: () => void }) {
  if (!hasMore) return null;
  return (
    <div className="pt-3 pb-1 flex justify-center">
      <button
        type="button"
        disabled={loading}
        onClick={onClick}
        className="h-7 px-3 rounded-full border border-line bg-card hover:bg-subtle text-[12.5px] font-medium text-fg-2 inline-flex items-center gap-1.5 transition-colors disabled:opacity-60"
      >
        {loading && <Loader2 className="size-3.5 animate-spin" />}
        {loading ? "Loading…" : "Load earlier messages"}
      </button>
    </div>
  );
}
