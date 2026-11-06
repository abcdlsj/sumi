import { describe, expect, it } from "vitest";

import type { Message, MessagePage } from "../../api/client";
import { timelinePage, upsertRootMessage, type MessagePages } from "./messageCache";

function root(seq: number, replyCount = 0): Message {
  return { id: `m${seq}`, thread_id: `m${seq}`, placement: "root", seq, reply_count: replyCount } as Message;
}

function page(seqs: number[], hasMoreBefore: boolean): MessagePage {
  return {
    channel_id: "c",
    snapshot_channel_seq: Math.max(...seqs),
    messages: seqs.map((seq) => root(seq)),
    has_more_before: hasMoreBefore,
    has_more_after: false,
  };
}

// Newest page first, then the page before it.
const loaded: MessagePages = { pages: [page([5, 7], true), page([2, 3], true)], pageParams: [undefined, 5] };

describe("Channel message cache", () => {
  it("reads loaded pages as one timeline, oldest first", () => {
    const timeline = timelinePage(loaded);
    expect(timeline?.messages.map((message) => message.seq)).toEqual([2, 3, 5, 7]);
    expect(timeline?.has_more_before).toBe(true);
  });

  it("replaces a loaded Root in place", () => {
    const updated = upsertRootMessage(loaded, root(3, 2));
    expect(timelinePage(updated)?.messages.find((message) => message.seq === 3)?.reply_count).toBe(2);
    expect(timelinePage(updated)?.messages).toHaveLength(4);
  });

  it("adds a new Root in order and leaves one older than every loaded page out", () => {
    const added = upsertRootMessage(loaded, root(6));
    expect(timelinePage(added)?.messages.map((message) => message.seq)).toEqual([2, 3, 5, 6, 7]);
    expect(upsertRootMessage(loaded, root(1))).toBe(loaded);
  });
});
