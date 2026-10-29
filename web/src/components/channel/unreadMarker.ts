import { useEffect, useState } from "react";

import type { Message } from "../../api/client";

const storageKey = (channelId: string) => `sumi.last-read.${channelId}`;

function readLastRead(channelId: string): string | null {
  try {
    return window.localStorage.getItem(storageKey(channelId));
  } catch {
    return null;
  }
}

/**
 * The first message that arrived since this browser last showed the
 * Channel. It is fixed when the Channel opens, so the divider stays put
 * while new messages keep arriving and being read.
 */
export function useUnreadMarker(channelId: string, messages: readonly Message[] | undefined, selfMemberId: string | undefined): string | undefined {
  const [marker, setMarker] = useState<{ channelId: string; messageId?: string } | null>(null);
  if (messages && marker?.channelId !== channelId) {
    const lastRead = readLastRead(channelId);
    const first = lastRead
      ? messages.find((message) => message.created_at > lastRead && message.author.id !== selfMemberId && message.content.type !== "system_notice")
      : undefined;
    setMarker({ channelId, messageId: first?.id });
  }

  const latest = messages?.at(-1)?.created_at;
  useEffect(() => {
    if (!latest) return;
    try {
      const stored = window.localStorage.getItem(storageKey(channelId));
      if (!stored || stored < latest) window.localStorage.setItem(storageKey(channelId), latest);
    } catch {
      // Storage can be unavailable; the divider is only a convenience.
    }
  }, [channelId, latest]);

  return marker?.channelId === channelId ? marker.messageId : undefined;
}
