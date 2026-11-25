import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { ArrowDown, Loader2 } from "lucide-react";
import { api, type AgentReadProgress, type Message } from "../api";

export interface MessagePage {
  messages: Message[];
  read_seq: number;
  following: boolean;
  has_more?: boolean;
  agent_reads?: AgentReadProgress[];
}

/** Merges a freshly-sent message into the tail cache immediately so it doesn't vanish between POST and refetch. */
export function mergeMessageIntoCache(qc: QueryClient, spaceSlug: string, convID: string, message: Message) {
  qc.setQueryData<MessagePage>(["messages", spaceSlug, convID], (old) => {
    if (!old) return old;
    if (old.messages.some((m) => m.id === message.id || m.seq === message.seq)) return old;
    const next = [...old.messages, message].sort((a, b) => a.seq - b.seq);
    return { ...old, messages: next };
  });
}

/** Distance from the end that still counts as reading the newest message. */
const NEAR_END_PX = 80;
/** Distance scrolled up before showing a calm "Jump to latest" pill even with no new arrivals. */
const JUMP_TO_LATEST_PX = 360;

function distanceFromEnd(el: HTMLElement): number {
  return el.scrollHeight - el.scrollTop - el.clientHeight;
}

export interface StickToBottomOptions {
  /** Identity of the stream; switching streams re-places the view. */
  streamKey: string;
  /** Messages currently rendered. */
  count: number;
  /** Highest seq in the live tail so fixed-size sliding windows (>=100) still detect new arrivals. */
  lastSeq?: number;
  /** Identity of the newest message so cache replacements while pinned keep the end in view. */
  lastId?: string;
  /** Element id of the persisted unread divider, used for the first placement only. */
  anchorId?: string;
}

export interface StickToBottom {
  ref: React.RefObject<HTMLDivElement | null>;
  /** Attach to the scroller's `onScroll`. */
  onScroll: () => void;
  /** Messages that arrived while the reader was above the end. */
  newCount: number;
  /** True when the reader scrolled far above the latest message. */
  farFromEnd: boolean;
  /** Jump to the end and clear the counter. */
  jumpToEnd: () => void;
  /** Records a programmatic scroll adjustment (e.g. prepending older pages) so it isn't misread as user scrolling. */
  syncScrollTop: (el: HTMLElement) => void;
}

/**
 * Keeps a message stream glued to its end while the reader stays there. Scrolling up
 * parks the stream and counts later arrivals instead of yanking the view; the first
 * placement of a stream lands on the unread divider when there is one, else the end.
 */
export function useStickToBottom({ streamKey, count, lastSeq, lastId, anchorId }: StickToBottomOptions): StickToBottom {
  const ref = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);
  const placed = useRef(false);
  const anchored = useRef(false);
  const seenCount = useRef(0);
  const seenSeq = useRef(0);
  const lastTop = useRef(0);
  const moved = useRef(false);
  const [newCount, setNewCount] = useState(0);
  const [farFromEnd, setFarFromEnd] = useState(false);

  /** Records our own scroll positions so they are not read as the reader moving. */
  const setTop = useCallback((el: HTMLElement, top: number) => {
    el.scrollTop = top;
    lastTop.current = el.scrollTop;
  }, []);

  const syncScrollTop = useCallback((el: HTMLElement) => {
    lastTop.current = el.scrollTop;
  }, []);

  const jumpToEnd = useCallback(() => {
    const el = ref.current;
    if (el) setTop(el, el.scrollHeight);
    pinned.current = true;
    setNewCount(0);
    setFarFromEnd(false);
  }, [setTop]);

  useLayoutEffect(() => {
    pinned.current = true;
    placed.current = false;
    anchored.current = false;
    seenCount.current = 0;
    seenSeq.current = 0;
    lastTop.current = 0;
    moved.current = false;
    setNewCount(0);
    setFarFromEnd(false);
  }, [streamKey]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || count === 0) return;
    let grew = 0;
    if (lastSeq !== undefined && lastSeq > 0) {
      grew = seenSeq.current > 0 ? Math.max(0, lastSeq - seenSeq.current) : 0;
      seenSeq.current = Math.max(seenSeq.current, lastSeq);
      seenCount.current = count;
    } else {
      grew = Math.max(0, count - seenCount.current);
      seenCount.current = count;
    }
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
  }, [count, lastSeq, lastId, anchorId, setTop]);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    if (Math.abs(el.scrollTop - lastTop.current) > 1) moved.current = true;
    const dist = distanceFromEnd(el);
    const atEnd = dist < NEAR_END_PX;
    pinned.current = atEnd;
    setFarFromEnd(dist > JUMP_TO_LATEST_PX);
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
        if (!box || !placed.current || !pinned.current) return;
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

  return { ref, onScroll, newCount, farFromEnd, jumpToEnd, syncScrollTop };
}

/** Floating pill shown while parked above the end: accented for new arrivals, calm for history browsing. */
export function NewMessagesPill({
  count,
  farFromEnd = false,
  onClick,
}: {
  count: number;
  farFromEnd?: boolean;
  onClick: () => void;
}) {
  if (count <= 0 && !farFromEnd) return null;
  const hasNew = count > 0;
  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center z-20">
      <button
        type="button"
        onClick={onClick}
        className={`pointer-events-auto h-7 pl-3 pr-2.5 rounded-[5px] shadow-md text-[12.5px] font-medium inline-flex items-center gap-1.5 animate-rise-in transition-colors ${
          hasNew
            ? "bg-accent text-accent-fg hover:bg-accent-hover"
            : "bg-card text-fg-2 border border-line-strong hover:bg-subtle hover:text-fg"
        }`}
      >
        {hasNew ? `${count} new ${count === 1 ? "message" : "messages"}` : "Jump to latest"}
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
  onAdjustScroll,
}: {
  spaceSlug: string;
  convID?: string;
  latest?: Message[];
  latestHasMore?: boolean;
  scrollRef: React.RefObject<HTMLDivElement | null>;
  onAdjustScroll?: (el: HTMLElement) => void;
}) {
  const [older, setOlder] = useState<Message[]>([]);
  const [hasOlder, setHasOlder] = useState<boolean | null>(null);
  const [loading, setLoading] = useState(false);
  const pendingHeight = useRef<number | null>(null);
  const activeConvRef = useRef(convID);
  activeConvRef.current = convID;

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
    if (delta > 0) {
      el.scrollTop += delta;
      onAdjustScroll?.(el);
    }
    pendingHeight.current = null;
  }, [older, scrollRef, onAdjustScroll]);

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
    const requestConvID = convID;
    setLoading(true);
    try {
      const page = await api.listMessages(spaceSlug, requestConvID, false, { beforeSeq: oldest.seq });
      if (activeConvRef.current !== requestConvID) return;
      if (scrollRef.current) pendingHeight.current = scrollRef.current.scrollHeight;
      setOlder((prev) => {
        const seen = new Set(prev.map((m) => m.id));
        return [...page.messages.filter((m) => !seen.has(m.id)), ...prev];
      });
      setHasOlder(Boolean(page.has_more));
    } finally {
      if (activeConvRef.current === requestConvID) setLoading(false);
    }
  }, [spaceSlug, convID, loading, hasMore, messages, scrollRef]);

  const patchMessage = useCallback((messageID: string, patch: (m: Message) => Message) => {
    setOlder((prev) => (prev.some((m) => m.id === messageID) ? prev.map((m) => (m.id === messageID ? patch(m) : m)) : prev));
  }, []);

  return { messages, hasMore, loading, loadEarlier, patchMessage };
}

export function LoadEarlierButton({ hasMore, loading, onClick }: { hasMore: boolean; loading: boolean; onClick: () => void }) {
  if (!hasMore) return null;
  return (
    <div className="pt-3 pb-1 flex justify-center">
      <button
        type="button"
        disabled={loading}
        onClick={onClick}
        className="h-7 px-3 rounded-[6px] border border-line bg-card hover:bg-subtle text-[12.5px] font-medium text-fg-2 inline-flex items-center gap-1.5 transition-colors disabled:opacity-60"
      >
        {loading && <Loader2 className="size-3.5 animate-spin" />}
        {loading ? "Loading…" : "Load earlier messages"}
      </button>
    </div>
  );
}
