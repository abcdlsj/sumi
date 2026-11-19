import { useCallback, useEffect, useRef, useState } from "react";
import { v7 as uuidv7 } from "uuid";

import type { Attachment, CreateMessageInput, Message } from "../../api/types";

/** A message the reader sent that the Server has not confirmed yet. */
export interface OutgoingMessage {
  key: string;
  /** Fixed at queue time so Retry cannot post the message twice. */
  idempotencyKey: string;
  input: CreateMessageInput;
  attachments?: Attachment[];
  createdAt: string;
  status: "sending" | "failed";
  error?: string;
}

type Delivery = Pick<OutgoingMessage, "key" | "idempotencyKey" | "input">;

// Persist pending/failed outbox items per conversation scope across route switches
// and Thread closes so unsent or failed messages are never silently discarded.
const scopedOutboxStore = new Map<string, OutgoingMessage[]>();
const scopedListeners = new Map<string, Set<() => void>>();

function readScope(scopeKey: string): OutgoingMessage[] {
  return scopedOutboxStore.get(scopeKey) ?? [];
}

function updateScope(scopeKey: string, updater: (current: OutgoingMessage[]) => OutgoingMessage[]) {
  const next = updater(readScope(scopeKey));
  if (next.length === 0) {
    scopedOutboxStore.delete(scopeKey);
  } else {
    scopedOutboxStore.set(scopeKey, next);
  }
  for (const listener of scopedListeners.get(scopeKey) ?? []) {
    listener();
  }
}

/**
 * Sends messages optimistically: each one shows in the timeline at once and
 * is replaced by the Server's copy when it lands, or stays with Retry and
 * Discard when it fails, so nothing the reader typed is lost.
 */
export function useOutbox(
  send: (input: CreateMessageInput, idempotencyKey: string) => Promise<Message>,
  onSent: (message: Message) => void,
  scopeKey?: string,
) {
  const [localItems, setLocalItems] = useState<OutgoingMessage[]>(() =>
    scopeKey ? readScope(scopeKey) : [],
  );
  const counter = useRef(0);
  const sendRef = useRef(send);
  const onSentRef = useRef(onSent);
  useEffect(() => {
    sendRef.current = send;
    onSentRef.current = onSent;
  });

  useEffect(() => {
    if (!scopeKey) return;
    setLocalItems(readScope(scopeKey));
    const sync = () => setLocalItems(readScope(scopeKey));
    let set = scopedListeners.get(scopeKey);
    if (!set) {
      set = new Set();
      scopedListeners.set(scopeKey, set);
    }
    set.add(sync);
    return () => {
      set?.delete(sync);
      if (set?.size === 0) scopedListeners.delete(scopeKey);
    };
  }, [scopeKey]);

  const setItems = useCallback(
    (updater: (current: OutgoingMessage[]) => OutgoingMessage[]) => {
      if (scopeKey) {
        updateScope(scopeKey, updater);
      } else {
        setLocalItems(updater);
      }
    },
    [scopeKey],
  );

  const deliver = useCallback((outgoing: Delivery) => {
    sendRef.current(outgoing.input, outgoing.idempotencyKey).then(
      (message) => {
        onSentRef.current(message);
        setItems((current) => current.filter((item) => item.key !== outgoing.key));
      },
      (error: unknown) => {
        const reason = error instanceof Error ? error.message : "The message was not sent.";
        setItems((current) => current.map((item) => (item.key === outgoing.key ? { ...item, status: "failed", error: reason } : item)));
      },
    );
  }, [setItems]);

  const queue = useCallback((input: CreateMessageInput, attachments?: Attachment[]) => {
    counter.current += 1;
    const key = `outgoing-${Date.now().toString(36)}-${counter.current}`;
    const outgoing: OutgoingMessage = {
      key,
      idempotencyKey: uuidv7(),
      input,
      attachments,
      createdAt: new Date().toISOString(),
      status: "sending",
    };
    setItems((current) => [...current, outgoing]);
    deliver(outgoing);
  }, [deliver, setItems]);

  const retry = useCallback((outgoing: OutgoingMessage) => {
    setItems((current) => current.map((item) => (item.key === outgoing.key ? { ...item, status: "sending", error: undefined } : item)));
    deliver(outgoing);
  }, [deliver, setItems]);

  const discard = useCallback((key: string) => {
    setItems((current) => current.filter((item) => item.key !== key));
  }, [setItems]);

  return { items: localItems, queue, retry, discard };
}

/**
 * Hides optimistic outgoing items whose server-confirmed twin has already arrived
 * over SSE, preventing duplicate rows when SSE wins the race against the POST response.
 */
export function filterConfirmedOutgoing(
  outgoing: readonly OutgoingMessage[],
  confirmed: readonly Message[],
  selfMemberId: string | undefined,
): OutgoingMessage[] {
  if (!selfMemberId || outgoing.length === 0 || confirmed.length === 0) {
    return [...outgoing];
  }
  return outgoing.filter((item) => {
    const queuedAt = Date.parse(item.createdAt);
    const body = item.input.body_markdown.trim();
    return !confirmed.some((msg) => {
      if (msg.author.id !== selfMemberId || msg.content.type !== "text") return false;
      if (msg.content.body_markdown.trim() !== body) return false;
      const landedAt = Date.parse(msg.created_at);
      if (!Number.isFinite(queuedAt) || !Number.isFinite(landedAt)) return false;
      return Math.abs(landedAt - queuedAt) < 120_000;
    });
  });
}

/** A timeline-shaped stand-in for an outgoing message, so grouping matches the real one. */
export function provisionalMessage(item: OutgoingMessage, author: Message["author"], channelId: string): Message {
  return {
    id: item.key,
    seq: 0,
    channel_id: channelId,
    thread_id: "",
    placement: "root",
    reply_count: 0,
    latest_replies: [],
    author,
    content: { type: "text", body_markdown: item.input.body_markdown },
    mentions: item.input.mentions ?? [],
    mention_all: item.input.mention_all ?? false,
    attachments: item.attachments ?? [],
    attention_failures: [],
    task_refs: [],
    created_at: item.createdAt,
  };
}
