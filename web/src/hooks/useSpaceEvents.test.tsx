import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useSpaceEvents } from "./useSpaceEvents";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  readonly listeners = new Map<string, Set<EventListener>>();
  readyState = 0;
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(readonly url: string) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? new Set<EventListener>();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: EventListener) {
    this.listeners.get(type)?.delete(listener);
  }

  close() {}

  emit(type: string, payload: unknown) {
    const event = { data: JSON.stringify(payload) } as MessageEvent<string>;
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

afterEach(() => {
  cleanup();
  FakeEventSource.instances = [];
  vi.unstubAllGlobals();
});

describe("useSpaceEvents connection", () => {
  it("reports a dropped stream and refetches everything once it is back", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    render(
      <QueryClientProvider client={queryClient}>
        <ConnectionProbe spaceId="space-1" />
      </QueryClientProvider>,
    );
    const source = FakeEventSource.instances[0];

    act(() => { source.readyState = 1; source.onopen?.(); });
    expect(screen.getByLabelText("Connection")).toHaveTextContent("live");
    expect(invalidate).not.toHaveBeenCalled();

    act(() => { source.readyState = 0; source.onerror?.(); });
    expect(screen.getByLabelText("Connection")).toHaveTextContent("reconnecting");

    act(() => { source.readyState = 1; source.onopen?.(); });
    expect(screen.getByLabelText("Connection")).toHaveTextContent("live");
    expect(invalidate).toHaveBeenCalledWith();
  });
});

function ConnectionProbe({ spaceId }: { spaceId: string }) {
  const { connection } = useSpaceEvents(spaceId);
  return <span aria-label="Connection">{connection}</span>;
}

describe("useSpaceEvents", () => {
  it("refetches the affected Channel members after a membership event", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let members = ["Ada"];
    const fetchMembers = vi.fn(async () => members);

    render(
      <QueryClientProvider client={queryClient}>
        <MembersProbe spaceId="space-1" channelId="channel-1" fetchMembers={fetchMembers} />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByLabelText("Channel members")).toHaveTextContent("Ada"));
    expect(FakeEventSource.instances).toHaveLength(1);

    members = ["Ada", "Grace"];
    FakeEventSource.instances[0].emit("member.changed", {
      type: "member.changed",
      occurred_at: new Date(Date.now() + 1_000).toISOString(),
      data: { channel_id: "channel-1", resource_id: "member-grace" },
    });

    await waitFor(() => expect(screen.getByLabelText("Channel members")).toHaveTextContent("Ada,Grace"));
    expect(fetchMembers).toHaveBeenCalledTimes(2);
  });
});

function MembersProbe({
  spaceId,
  channelId,
  fetchMembers,
}: {
  spaceId: string;
  channelId: string;
  fetchMembers: () => Promise<string[]>;
}) {
  useSpaceEvents(spaceId);
  const query = useQuery({
    queryKey: ["channel-members", channelId],
    queryFn: fetchMembers,
  });
  return <output aria-label="Channel members">{query.data?.join(",") ?? ""}</output>;
}
