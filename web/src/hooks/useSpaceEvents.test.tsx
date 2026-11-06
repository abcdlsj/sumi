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
  it("refetches everything on every open, and reports a dropped stream", async () => {
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
    // The stream starts at the Server tail, so the first open also has to refetch.
    expect(invalidate).toHaveBeenCalledWith();

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

  it("refetches the Agent roster and the Member list after an Agent is created", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let members = ["Ada"];
    let agents = ["Lin"];
    const fetchMembers = vi.fn(async () => members);
    const fetchAgents = vi.fn(async () => agents);

    render(
      <QueryClientProvider client={queryClient}>
        <AgentRosterProbe spaceId="space-1" fetchMembers={fetchMembers} fetchAgents={fetchAgents} />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByLabelText("Members")).toHaveTextContent("Ada"));
    expect(screen.getByLabelText("Agents")).toHaveTextContent("Lin");

    members = ["Ada", "Mira"];
    agents = ["Lin", "Mira"];
    FakeEventSource.instances[0].emit("agent.created", {
      type: "agent.created",
      occurred_at: new Date(Date.now() + 1_000).toISOString(),
      data: { member_id: "019c0000-0000-7000-8000-000000000040" },
    });

    await waitFor(() => expect(screen.getByLabelText("Members")).toHaveTextContent("Ada,Mira"));
    expect(screen.getByLabelText("Agents")).toHaveTextContent("Lin,Mira");
  });
  it("treats an event stamped before the connection opened as live", async () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    let messages = ["first"];
    const fetchMessages = vi.fn(async () => messages);
    const onMessage = vi.fn();

    render(
      <QueryClientProvider client={queryClient}>
        <MessagesProbe
          spaceId="space-1"
          channelId="channel-1"
          fetchMessages={fetchMessages}
          onMessage={onMessage}
        />
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByLabelText("Messages")).toHaveTextContent("first"));
    expect(onMessage).not.toHaveBeenCalled();

    messages = ["first", "second"];
    FakeEventSource.instances[0].emit("message.created", {
      type: "message.created",
      occurred_at: new Date(Date.now() - 24 * 60 * 60 * 1000).toISOString(),
      data: { channel_id: "channel-1" },
    });

    await waitFor(() => expect(screen.getByLabelText("Messages")).toHaveTextContent("first,second"));
    expect(onMessage).toHaveBeenCalledWith({ type: "message.created", channelId: "channel-1" });
  });
});

function MessagesProbe({
  spaceId,
  channelId,
  fetchMessages,
  onMessage,
}: {
  spaceId: string;
  channelId: string;
  fetchMessages: () => Promise<string[]>;
  onMessage: (event: { type: string; channelId: string }) => void;
}) {
  useSpaceEvents(spaceId, onMessage);
  const query = useQuery({ queryKey: ["messages", channelId], queryFn: fetchMessages });
  return <output aria-label="Messages">{query.data?.join(",") ?? ""}</output>;
}

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

function AgentRosterProbe({
  spaceId,
  fetchMembers,
  fetchAgents,
}: {
  spaceId: string;
  fetchMembers: () => Promise<string[]>;
  fetchAgents: () => Promise<string[]>;
}) {
  useSpaceEvents(spaceId);
  const members = useQuery({ queryKey: ["members", spaceId], queryFn: fetchMembers });
  const agents = useQuery({ queryKey: ["agents", spaceId], queryFn: fetchAgents });
  return (
    <>
      <output aria-label="Members">{members.data?.join(",") ?? ""}</output>
      <output aria-label="Agents">{agents.data?.join(",") ?? ""}</output>
    </>
  );
}
