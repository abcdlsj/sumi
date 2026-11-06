import { infiniteQueryOptions, type InfiniteData, type QueryClient } from "@tanstack/react-query";

import {getMessage, listMessages} from "../../api/client";
import type {Message, MessagePage} from "../../api/types";

/** Pages run newest first; each later page holds the Root Messages before the previous one. */
export type MessagePages = InfiniteData<MessagePage, unknown>;

export function messagesQueryOptions(channelId: string) {
  return infiniteQueryOptions({
    queryKey: ["messages", channelId],
    queryFn: ({ pageParam }) => listMessages(channelId, { beforeSeq: pageParam }),
    initialPageParam: undefined as number | undefined,
    // A refetch recomputes every later page from the one before it, so a new Root Message that
    // pushes an older one off the newest page cannot leave a gap between pages.
    getNextPageParam: (page) => (page.has_more_before ? page.messages[0]?.seq : undefined),
  });
}

/** The loaded pages as one timeline, oldest first. */
export function timelinePage(data: MessagePages | undefined): MessagePage | undefined {
  if (!data || data.pages.length === 0) return undefined;
  const [newest] = data.pages;
  const oldest = data.pages[data.pages.length - 1];
  return {
    channel_id: newest.channel_id,
    snapshot_channel_seq: newest.snapshot_channel_seq,
    messages: [...data.pages].reverse().flatMap((page) => page.messages),
    has_more_before: oldest.has_more_before,
    has_more_after: false,
  };
}

/** Replaces a loaded Root Message, or adds a new one to the newest page in `seq` order. */
export function upsertRootMessage(data: MessagePages | undefined, message: Message): MessagePages | undefined {
  if (!data || data.pages.length === 0 || message.placement !== "root") return data;
  if (data.pages.some((page) => page.messages.some((item) => item.id === message.id))) {
    return {
      ...data,
      pages: data.pages.map((page) => ({
        ...page,
        messages: page.messages.map((item) => (item.id === message.id ? message : item)),
      })),
    };
  }
  const [newest, ...older] = data.pages;
  // A Root older than everything loaded belongs to a page the reader has not opened.
  const oldestLoaded = data.pages[data.pages.length - 1].messages[0]?.seq;
  if (oldestLoaded !== undefined && message.seq < oldestLoaded) return data;
  return {
    ...data,
    pages: [
      {
        ...newest,
        snapshot_channel_seq: Math.max(newest.snapshot_channel_seq, message.seq),
        messages: [...newest.messages, message].sort((a, b) => a.seq - b.seq),
      },
      ...older,
    ],
  };
}

/** Applies `update` to one loaded Root Message. */
export function updateRootMessage(
  data: MessagePages | undefined,
  messageId: string,
  update: (message: Message) => Message,
): MessagePages | undefined {
  if (!data) return data;
  return {
    ...data,
    pages: data.pages.map((page) => ({
      ...page,
      messages: page.messages.map((item) => (item.id === messageId ? update(item) : item)),
    })),
  };
}

/**
 * Re-reads one Root Message after a change in its Thread and patches it into the open timeline.
 * A reply changes only its Root's reply count and preview, so the rest of the Channel is not read.
 */
export async function refreshRootMessage(queryClient: QueryClient, channelId: string, threadId: string): Promise<void> {
  const key = messagesQueryOptions(channelId).queryKey;
  if (!queryClient.getQueryData(key)) return;
  const root = await getMessage(threadId);
  queryClient.setQueryData<MessagePages>(key, (data) => upsertRootMessage(data, root));
}
