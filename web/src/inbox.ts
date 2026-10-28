import type { InboxItem } from "./api/client";

export type InboxStack = {
  id: string;
  kind: "dm" | "thread";
  items: InboxItem[];
  latest: InboxItem;
};

const humanInboxKinds = new Set<InboxItem["kind"]>([
  "direct",
  "mention",
  "reply",
  "task_activity",
  "thread_activity",
]);

/** Collapses Inbox Items into one stack per DM or Thread, newest first. */
export function groupInboxItems(items: InboxItem[]): InboxStack[] {
  const stacks = new Map<string, InboxStack>();
  for (const item of items) {
    if (!humanInboxKinds.has(item.kind)) continue;
    const kind = item.kind === "direct" ? "dm" : "thread";
    const targetId = kind === "dm" ? item.channel_id : item.thread_id;
    const id = `${item.space_id}:${kind}:${targetId ?? item.id}`;
    const stack = stacks.get(id);
    if (stack) {
      stack.items.push(item);
    } else {
      stacks.set(id, { id, kind, items: [item], latest: item });
    }
  }
  return [...stacks.values()]
    .map((stack) => {
      const sorted = [...stack.items].sort((left, right) =>
        Date.parse(right.created_at) - Date.parse(left.created_at),
      );
      return { ...stack, items: sorted, latest: sorted[0] };
    })
    .sort((left, right) => Date.parse(right.latest.created_at) - Date.parse(left.latest.created_at));
}
