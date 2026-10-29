import { useCallback, useEffect, useRef, useState } from "react";

import type { Message } from "../../api/client";
import type { ComposerInput } from "./MessageComposer";

/** A message the reader sent that the Server has not confirmed yet. */
export interface OutgoingMessage {
  key: string;
  input: ComposerInput;
  createdAt: string;
  status: "sending" | "failed";
  error?: string;
}

/**
 * Sends messages optimistically: each one shows in the timeline at once and
 * is replaced by the Server's copy when it lands, or stays with Retry and
 * Discard when it fails, so nothing the reader typed is lost.
 */
export function useOutbox(send: (input: ComposerInput) => Promise<Message>, onSent: (message: Message) => void) {
  const [items, setItems] = useState<OutgoingMessage[]>([]);
  const counter = useRef(0);
  const sendRef = useRef(send);
  const onSentRef = useRef(onSent);
  useEffect(() => {
    sendRef.current = send;
    onSentRef.current = onSent;
  });

  const deliver = useCallback((key: string, input: ComposerInput) => {
    sendRef.current(input).then(
      (message) => {
        onSentRef.current(message);
        setItems((current) => current.filter((item) => item.key !== key));
      },
      (error: unknown) => {
        const reason = error instanceof Error ? error.message : "The message was not sent.";
        setItems((current) => current.map((item) => (item.key === key ? { ...item, status: "failed", error: reason } : item)));
      },
    );
  }, []);

  const queue = useCallback((input: ComposerInput) => {
    counter.current += 1;
    const key = `outgoing-${Date.now().toString(36)}-${counter.current}`;
    setItems((current) => [...current, { key, input, createdAt: new Date().toISOString(), status: "sending" }]);
    deliver(key, input);
  }, [deliver]);

  const retry = useCallback((outgoing: OutgoingMessage) => {
    setItems((current) => current.map((item) => (item.key === outgoing.key ? { ...item, status: "sending", error: undefined } : item)));
    deliver(outgoing.key, outgoing.input);
  }, [deliver]);

  const discard = useCallback((key: string) => {
    setItems((current) => current.filter((item) => item.key !== key));
  }, []);

  return { items, queue, retry, discard };
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
    author,
    content: { type: "text", body_markdown: item.input.body_markdown },
    mentions: item.input.mentions,
    mention_all: item.input.mention_all,
    attachments: [],
    attention_failures: [],
    task_refs: [],
    created_at: item.createdAt,
  };
}
