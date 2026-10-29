import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import { openSpaceEvents } from "../api/client";
import { clearAgentActivityForChannel, recordAgentActivity, type AgentActivityEvent } from "../agentActivity";

interface SumiEvent {
  type: string;
  event_id?: string;
  occurred_at?: string;
  data: {
    channel_id?: string;
    member_id?: string;
    kind?: string;
    run_id?: string;
    thread_id?: string;
    message_id?: string;
    task_id?: string;
    item_id?: string;
    target_member_id?: string;
    scope_channel_id?: string;
    arguments?: { name: string; value: string }[];
    message_preview?: string;
    message_truncated?: boolean;
  };
}

/** Whether this tab is receiving live updates for the Space. */
export type SpaceConnection = "connecting" | "live" | "reconnecting" | "offline";

const MAX_RETRY_MS = 30_000;
// EventSource.readyState values.
const OPEN = 1;
const CLOSED = 2;

export function useSpaceEvents(
  spaceId?: string,
  onMessage?: (event: { type: string; channelId: string }) => void,
): { connection: SpaceConnection; reconnect: () => void } {
  const queryClient = useQueryClient();
  const [connection, setConnection] = useState<SpaceConnection>("connecting");
  const [attempt, setAttempt] = useState(0);
  const onMessageRef = useRef(onMessage);
  useEffect(() => {
    onMessageRef.current = onMessage;
  });

  useEffect(() => {
    if (!spaceId || typeof EventSource === "undefined") return;
    // A fresh connection replays the storage retention window (historical
    // message.created etc.). Only events arriving after the connection opened
    // are live; replay must not be treated as new unread activity.
    const connectedAt = Date.now();
    let source = openSpaceEvents(spaceId);
    let wasLive = false;
    let lost = attempt > 0;
    let retryDelay = 1000;
    let retryTimer: number | undefined;
    const invalidate = (event: MessageEvent<string>) => {
      const payload = JSON.parse(event.data) as SumiEvent;
      const isReplay =
        payload.occurred_at !== undefined &&
        new Date(payload.occurred_at).getTime() < connectedAt;
      if (
        payload.type === "agent.activity" &&
        payload.event_id &&
        payload.occurred_at &&
        payload.data.member_id &&
        payload.data.kind
      ) {
        recordAgentActivity(payload as AgentActivityEvent);
      }
      // Membership changes are relevant even when replayed: they revoke any scoped
      // preview that is still held in this tab's ephemeral feed.
      if (payload.type === "member.changed" && payload.data.channel_id) {
        clearAgentActivityForChannel(payload.data.channel_id);
      }
      if (isReplay) return;
      if (payload.type.startsWith("message.") && payload.data.channel_id) {
        if (payload.type === "message.created" && payload.data.channel_id) {
          onMessageRef.current?.({ type: payload.type, channelId: payload.data.channel_id });
        }
        void queryClient.invalidateQueries({ queryKey: ["messages", payload.data.channel_id] });
        void queryClient.invalidateQueries({ queryKey: ["thread", payload.data.channel_id] });
      }
      if (payload.type === "inbox.changed") {
        void queryClient.invalidateQueries({ queryKey: ["inbox", spaceId] });
      }
      if (payload.type === "thread.updated") {
        void queryClient.invalidateQueries({ queryKey: ["thread"] });
        if (payload.data.channel_id) {
          void queryClient.invalidateQueries({ queryKey: ["messages", payload.data.channel_id] });
        }
      }
      if (payload.type.startsWith("channel.")) {
        void queryClient.invalidateQueries({ queryKey: ["channels", spaceId] });
        void queryClient.invalidateQueries({ queryKey: ["direct-messages", spaceId] });
      }
      if (payload.type === "member.changed") {
        void queryClient.invalidateQueries({ queryKey: ["members", spaceId] });
        if (payload.data.channel_id) {
          void queryClient.invalidateQueries({ queryKey: ["channel-members", payload.data.channel_id] });
        } else {
          // Space-level membership changes have no Channel scope. Keep the
          // fallback for any open Channel projections that may contain it.
          void queryClient.invalidateQueries({ queryKey: ["channel-members"] });
        }
      }
      if (payload.type === "agent.changed" || payload.type === "agent.updated" || payload.type === "run.changed") {
        void queryClient.invalidateQueries({ queryKey: ["agents", spaceId] });
        void queryClient.invalidateQueries({ queryKey: ["agent"] });
      }
      if (payload.type === "computer.changed") {
        void queryClient.invalidateQueries({ queryKey: ["computers", spaceId] });
      }
      if (payload.type.startsWith("task.")) {
        void queryClient.invalidateQueries({ queryKey: ["tasks", spaceId] });
        void queryClient.invalidateQueries({ queryKey: ["messages"] });
        void queryClient.invalidateQueries({ queryKey: ["thread"] });
      }
    };
    const eventTypes = [
      "message.created",
      "message.updated",
      "message.deleted",
      "thread.updated",
      "inbox.changed",
      "channel.created",
      "channel.updated",
      "member.changed",
      "agent.changed",
      "agent.created",
      "agent.updated",
      "agent.activity",
      "computer.changed",
      "run.changed",
      "task.created",
      "task.updated",
      "task.linked",
      "task.unlinked",
      "task.finished",
    ];
    const attach = (target: EventSource) => {
      for (const type of eventTypes) target.addEventListener(type, invalidate as EventListener);
      target.onopen = () => {
        retryDelay = 1000;
        // Anything that changed while the stream was down has no event here;
        // refetch what is on screen so nothing stays stale.
        if (lost) void queryClient.invalidateQueries();
        lost = false;
        wasLive = true;
        setConnection("live");
      };
      target.onerror = () => {
        lost = wasLive || lost;
        setConnection(navigator.onLine === false ? "offline" : "reconnecting");
        // The browser retries a dropped stream by itself; a closed one needs a new connection.
        if (target.readyState === CLOSED) {
          lost = true;
          window.clearTimeout(retryTimer);
          retryTimer = window.setTimeout(() => {
            source = openSpaceEvents(spaceId);
            attach(source);
          }, retryDelay);
          retryDelay = Math.min(retryDelay * 2, MAX_RETRY_MS);
        }
      };
    };
    attach(source);
    const goOffline = () => setConnection("offline");
    const goOnline = () => {
      if (source.readyState === OPEN) return;
      setConnection("reconnecting");
      source.close();
      lost = true;
      window.clearTimeout(retryTimer);
      source = openSpaceEvents(spaceId);
      attach(source);
    };
    window.addEventListener("offline", goOffline);
    window.addEventListener("online", goOnline);
    return () => {
      window.clearTimeout(retryTimer);
      window.removeEventListener("offline", goOffline);
      window.removeEventListener("online", goOnline);
      source.close();
    };
  }, [attempt, queryClient, spaceId]);

  return { connection, reconnect: () => setAttempt((value) => value + 1) };
}
