import { useCallback, useEffect, useRef, useState } from "react";
import { v7 as uuidv7 } from "uuid";

import type {CreateMessageInput, Message} from "../../api/types";

/** A message the reader sent that the Server has not confirmed yet. */
export interface OutgoingMessage {
  key: string;
  /** Fixed at queue time so Retry cannot post the message twice. */
  idempotencyKey: string;
  input: CreateMessageInput;
  createdAt: string;
  status: "sending" | "failed";
  error?: string;
}

type Delivery = Pick<OutgoingMessage, "key" | "idempotencyKey" | "input">;

/**
 * Sends messages optimistically: each one shows in the timeline at once and
 * is replaced by the Server's copy when it lands, or stays with Retry and
 * Discard when it fails, so nothing the reader typed is lost.
 */
export function useOutbox(
  send: (input: CreateMessageInput, idempotencyKey: string) => Promise<Message>,
  onSent: (message: Message) => void,
) {
  const [items, setItems] = useState<OutgoingMessage[]>([]);
  const counter = useRef(0);
  const sendRef = useRef(send);
  const onSentRef = useRef(onSent);
  useEffect(() => {
    sendRef.current = send;
    onSentRef.current = onSent;
  });

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
  }, []);

  const queue = useCallback((input: CreateMessageInput) => {
    counter.current += 1;
    const key = `outgoing-${Date.now().toString(36)}-${counter.current}`;
    const outgoing: OutgoingMessage = {
      key,
      idempotencyKey: uuidv7(),
      input,
      createdAt: new Date().toISOString(),
      status: "sending",
    };
    setItems((current) => [...current, outgoing]);
    deliver(outgoing);
  }, [deliver]);

  const retry = useCallback((outgoing: OutgoingMessage) => {
    setItems((current) => current.map((item) => (item.key === outgoing.key ? { ...item, status: "sending", error: undefined } : item)));
    deliver(outgoing);
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
    latest_replies: [],
    author,
    content: { type: "text", body_markdown: item.input.body_markdown },
    mentions: item.input.mentions ?? [],
    mention_all: item.input.mention_all ?? false,
    attachments: [],
    attention_failures: [],
    task_refs: [],
    created_at: item.createdAt,
  };
}
