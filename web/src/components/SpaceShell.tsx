import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation, useNavigate } from "@tanstack/react-router";
import {
  Hash,
  Building2,
  Inbox,
  LockKeyhole,
  ListTodo,
  LoaderCircle,
  Menu,
  Monitor,
  Network,
  Plus,
  Search,
  Settings,
  Users,
  X,
  type LucideIcon,
} from "lucide-react";
import type { FormEvent, ReactNode } from "react";
import { useEffect, useRef, useState } from "react";

import {
  ApiRequestError,
  createChannel,
  createDirectMessage,
  currentUser,
  getSpaceBySlug,
  joinChannel,
  listChannels,
  listAgents,
  listComputers,
  listDirectMessages,
  listInbox,
  listMembers,
  type Channel,
  type Agent,
  type Computer,
  type DirectMessage,
  type Member,
  type Space,
  type User,
} from "../api/client";
import { activityLabel } from "../agentActivity";
import {
  AGENT_INSIGHTS_FEATURE_ID,
  COMPANY_OFFICE_FEATURE_ID,
  useFeatureEnabled,
} from "../featureRegistry";
import { useSpaceEvents } from "../hooks/useSpaceEvents";
import { saveLastConversationRoute } from "../conversationMemory";
import { applySpaceAccent } from "../spaceAccent";
import { groupInboxItems } from "../inbox";
import { DialogFrame } from "./DialogFrame";
import { PixelIdentity } from "./PixelIdentity";
import { QuickSwitcher, type QuickSwitcherItem } from "./QuickSwitcher";
import { SumiMark } from "./SumiMark";
import { ShellSkeleton } from "./Skeleton";

export { PixelIdentity } from "./PixelIdentity";

export interface SpaceShellContext {
  space: Space;
  user: User;
  navigationOpen: boolean;
  channels: Channel[];
  directMessages: DirectMessage[];
  members: Member[];
  currentMember: Member;
}

export function SpaceShell({
  spaceSlug,
  active,
  children,
}: {
  spaceSlug: string;
  active: "channel" | "dm" | "company" | "members" | "agents" | "inbox" | "tasks" | "computers" | "insights" | "settings";
  children: (context: SpaceShellContext) => ReactNode;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const authenticationRedirect = useRef(location.href);
  const queryClient = useQueryClient();
  const [navigationOpen, setNavigationOpen] = useState(false);
  const [navigationTrigger, setNavigationTrigger] = useState<HTMLElement | null>(null);
  const [channelFormOpen, setChannelFormOpen] = useState(false);
  const [directMessageFormOpen, setDirectMessageFormOpen] = useState(false);
  const [switcherOpen, setSwitcherOpen] = useState(false);
  useEffect(() => {
    const handleKey = (event: KeyboardEvent) => {
      if (event.key.toLowerCase() !== "k" || !(event.metaKey || event.ctrlKey) || event.altKey || event.shiftKey) return;
      event.preventDefault();
      setSwitcherOpen((open) => open || !document.querySelector('[role="dialog"][aria-modal="true"]'));
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, []);
  const agentInsightsEnabled = useFeatureEnabled(AGENT_INSIGHTS_FEATURE_ID);
  const companyOfficeEnabled = useFeatureEnabled(COMPANY_OFFICE_FEATURE_ID);
  const [unreadChannelIds, setUnreadChannelIds] = useState<ReadonlySet<string>>(() => new Set());
  const navigationPanel = useRef<HTMLElement>(null);
  const topbarNavigationTrigger = useRef<HTMLButtonElement>(null);
  function closeNavigation() {
    setNavigationOpen(false);
    window.requestAnimationFrame(() => (navigationTrigger ?? topbarNavigationTrigger.current)?.focus());
  }
  function dismissNavigationDrawer() {
    setNavigationOpen(false);
  }
  function openNavigation() {
    setNavigationTrigger(document.activeElement as HTMLElement);
    setNavigationOpen(true);
  }
  const space = useQuery({
    queryKey: ["space", spaceSlug],
    queryFn: () => getSpaceBySlug(spaceSlug),
  });
  const user = useQuery({ queryKey: ["current-user"], queryFn: currentUser });
  const channels = useQuery({
    queryKey: ["channels", space.data?.id],
    queryFn: () => listChannels(space.data!.id),
    enabled: Boolean(space.data),
  });
  const directMessages = useQuery({
    queryKey: ["direct-messages", space.data?.id],
    queryFn: () => listDirectMessages(space.data!.id),
    enabled: Boolean(space.data),
  });
  const members = useQuery({
    queryKey: ["members", space.data?.id],
    queryFn: () => listMembers(space.data!.id),
    enabled: Boolean(space.data),
  });
  const agents = useQuery({
    queryKey: ["agents", space.data?.id],
    queryFn: () => listAgents(space.data!.id),
    enabled: Boolean(space.data),
    retry: false,
  });
  const computers = useQuery({
    queryKey: ["computers", space.data?.id],
    queryFn: () => listComputers(space.data!.id),
    enabled: active === "computers" && Boolean(space.data),
  });
  // Shares the Inbox page cache, so reading there clears the count here.
  const inbox = useQuery({
    queryKey: ["inbox", space.data?.id, space.data?.current_member_id],
    queryFn: () => listInbox(space.data!.current_member_id),
    enabled: Boolean(space.data),
    retry: false,
  });
  useSpaceEvents(space.data?.id, ({ channelId }) => {
    if (channelId === viewingChannelId(location.pathname, channels.data?.channels, directMessages.data)) {
      return;
    }
    setUnreadChannelIds((current) =>
      current.has(channelId) ? current : new Set(current).add(channelId),
    );
  });
  const channelCreation = useMutation({
    mutationFn: (input: Parameters<typeof createChannel>[1]) => createChannel(space.data!.id, input),
    onSuccess: (channel) => {
      void queryClient.invalidateQueries({ queryKey: ["channels", space.data?.id] });
      setChannelFormOpen(false);
      setNavigationOpen(false);
      void navigate({
        to: "/s/$spaceSlug/channels/$channelSlug",
        params: { spaceSlug: space.data!.slug, channelSlug: channel.slug },
      });
    },
  });
  const channelJoin = useMutation({
    mutationFn: joinChannel,
    onSuccess: (channel) => {
      void queryClient.invalidateQueries({ queryKey: ["channels", space.data?.id] });
      setNavigationOpen(false);
      void navigate({
        to: "/s/$spaceSlug/channels/$channelSlug",
        params: { spaceSlug: space.data!.slug, channelSlug: channel.slug },
      });
    },
  });
  const directMessageCreation = useMutation({
    mutationFn: (memberId: string) => createDirectMessage(space.data!.id, memberId),
    onSuccess: (directMessage) => {
      queryClient.setQueryData<DirectMessage[]>(
        ["direct-messages", space.data!.id],
        (current = []) => [
          ...current.filter((candidate) => candidate.channel_id !== directMessage.channel_id),
          directMessage,
        ],
      );
      setDirectMessageFormOpen(false);
      setNavigationOpen(false);
      void navigate({
        to: "/s/$spaceSlug/dm/$memberId",
        params: {
          spaceSlug: space.data!.slug,
          memberId: directMessage.other_member.id,
        },
      });
    },
  });
  const routeError =
    space.error ??
    user.error ??
    channels.error ??
    members.error ??
    (active === "computers" ? computers.error : undefined);

  const spaceAccent = space.data?.accent;
  useEffect(() => applySpaceAccent(spaceAccent), [spaceAccent]);

  useEffect(() => {
    if (routeError instanceof ApiRequestError && routeError.status === 401) {
      void navigate({
        to: "/login",
        search: { redirect: authenticationRedirect.current },
        replace: true,
      });
    }
  }, [navigate, routeError]);

  useEffect(() => {
    if (!navigationOpen) return;
    const panel = navigationPanel.current;
    const closeButton = panel?.querySelector<HTMLButtonElement>(".navigation-close");
    closeButton?.focus();
    const handleKey = (event: KeyboardEvent) => {
      if (document.querySelector('[role="dialog"][aria-modal="true"]')) return;
      if (event.key === "Escape") {
        setNavigationOpen(false);
        window.requestAnimationFrame(() => navigationTrigger?.focus());
        return;
      }
      const drawerMode = window.matchMedia?.("(max-width: 1099px)").matches ?? false;
      if (event.key !== "Tab" || !panel || !drawerMode) return;
      const focusable = [...panel.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), input, select, textarea')];
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [navigationOpen, navigationTrigger]);

  useEffect(() => {
    const channelMatch = location.pathname.match(/\/channels\/([^/]+)$/);
    if (channelMatch) {
      saveLastConversationRoute(spaceSlug, { kind: "channel", channelSlug: channelMatch[1] });
      return;
    }
    const dmMatch = location.pathname.match(/\/dm\/([^/]+)$/);
    if (dmMatch) {
      saveLastConversationRoute(spaceSlug, { kind: "dm", memberId: dmMatch[1] });
    }
  }, [location.pathname, spaceSlug]);

  // SPA navigation does not move focus on its own. After a route change,
  // focus the new view's h1 so keyboard users land at the content start.
  useEffect(() => {
    const heading = document.querySelector<HTMLElement>(".space-shell h1");
    if (!heading) return;
    heading.tabIndex = -1;
    heading.focus({ preventScroll: true });
  }, [location.pathname]);

  // Visiting a conversation clears its unread dot; everything else keeps it.
  useEffect(() => {
    const channelId = viewingChannelId(location.pathname, channels.data?.channels, directMessages.data);
    if (!channelId || !unreadChannelIds.has(channelId)) return;
    window.requestAnimationFrame(() => {
      setUnreadChannelIds((current) => {
        if (!current.has(channelId)) return current;
        const next = new Set(current);
        next.delete(channelId);
        return next;
      });
    });
  }, [location.pathname, channels.data, directMessages.data, unreadChannelIds]);

  if (
    space.isPending ||
    user.isPending ||
    channels.isPending ||
    members.isPending || (active === "computers" && computers.isPending)
  ) {
    return <ShellSkeleton />;
  }
  if (routeError) {
    return <RouteFailure error={routeError} retry={() => {
      void queryClient.invalidateQueries();
    }} />;
  }
  if (!space.data || !user.data || !channels.data || !members.data) {
    return <ShellSkeleton />;
  }
  const currentMember = members.data.find((member) => member.id === space.data.current_member_id);
  if (!currentMember) {
    return <div className="route-status route-status--error">Member identity unavailable.</div>;
  }
  const availableDirectMessages = directMessages.data ?? [];
  const activityByMemberId = new Map(
    (agents.data ?? []).map((agent) => [agent.member_id, agent.activity_status] as const),
  );
  const roleByMemberId = new Map(
    (agents.data ?? []).map((agent) => [agent.member_id, agent.role_text] as const),
  );
  const inboxCount = groupInboxItems(inbox.data ?? []).length;
  const spaceSlugValue = space.data.slug;
  const contextNavigation =
    active === "computers" && computers.data?.length ? (
      <ComputersNavigation
        computers={computers.data ?? []}
        spaceSlug={spaceSlugValue}
        activeHash={location.hash}
        canManage={["owner", "admin"].includes(currentMember.access_level)}
      />
    ) : null;
  const toolItem = (id: string, label: string, Icon: LucideIcon, path: string): QuickSwitcherItem => ({
    id: `tool-${id}`,
    label,
    group: "Go to",
    leading: <Icon aria-hidden="true" />,
    href: `/s/${spaceSlugValue}/${path}`,
  });
  const switcherItems: QuickSwitcherItem[] = [
    ...channels.data.channels.filter((channel) => channel.joined).map((channel) => ({
      id: `channel-${channel.id}`,
      label: channel.slug,
      group: "Channels",
      hint: channel.topic ?? undefined,
      leading: channel.kind === "private" ? <LockKeyhole aria-hidden="true" /> : <Hash aria-hidden="true" />,
      href: `/s/${spaceSlugValue}/channels/${channel.slug}`,
    })),
    ...availableDirectMessages.map((dm) => ({
      id: `dm-${dm.channel_id}`,
      label: dm.other_member.display_name,
      group: "Direct messages",
      hint: dm.other_member.kind === "agent" ? activityLabel(activityByMemberId.get(dm.other_member.id)) : undefined,
      leading: <PixelIdentity name={dm.other_member.display_name} kind={dm.other_member.kind} seed={dm.other_member.id} />,
      href: `/s/${spaceSlugValue}/dm/${dm.other_member.id}`,
    })),
    ...members.data.filter((member) => member.kind === "agent").map((member) => ({
      id: `agent-${member.id}`,
      label: member.display_name,
      group: "Agents",
      hint: roleByMemberId.get(member.id) ?? undefined,
      leading: <PixelIdentity name={member.display_name} kind="agent" seed={member.id} />,
      href: `/s/${spaceSlugValue}/agents/${member.id}`,
    })),
    toolItem("inbox", "Inbox", Inbox, "inbox"),
    toolItem("tasks", "Tasks", ListTodo, "tasks"),
    toolItem("members", "Members", Users, "members"),
    toolItem("computers", "Computers", Monitor, "computers"),
    toolItem("settings", "Settings", Settings, "settings"),
  ];
  const page = children({
    space: space.data,
    user: user.data,
    channels: channels.data.channels,
    directMessages: availableDirectMessages,
    members: members.data,
    currentMember,
    navigationOpen,
  });

  return (
    <>
      <a className="skip-link" href="#main-content">Skip to content</a>
      <main id="main-content" className="space-shell">
      <header className="space-topbar">
        <button
          ref={topbarNavigationTrigger}
          className="icon-button"
          type="button"
          aria-label="Open navigation"
          title="Open navigation"
          onClick={openNavigation}
        >
          <Menu />
        </button>
        <strong title={space.data.name}>{space.data.name}</strong>
        <button className="icon-button" type="button" aria-label="Jump to" title="Jump to" onClick={() => setSwitcherOpen(true)}>
          <Search />
        </button>
        <Link
          className="space-topbar-inbox"
          to="/s/$spaceSlug/inbox"
          params={{ spaceSlug: spaceSlugValue }}
          aria-label={inboxCount ? `Inbox, ${inboxCount} waiting for you` : "Inbox"}
        >
          <Inbox aria-hidden="true" />
          {inboxCount ? <span className="nav-count">{inboxCount}</span> : null}
        </Link>
      </header>

      {navigationOpen ? (
        <button
          className="navigation-scrim"
          type="button"
          aria-label="Close navigation"
          onClick={closeNavigation}
        />
      ) : null}
      <aside
        ref={navigationPanel}
        className={`space-navigation${navigationOpen ? " space-navigation--open" : ""}`}
        aria-label="Space navigation"
        onClick={(event) => {
          if ((event.target as HTMLElement).closest("a")) dismissNavigationDrawer();
        }}
      >
        <header className="space-header">
          <Link className="space-identity" to="/s/$spaceSlug" params={{ spaceSlug: spaceSlugValue }} title={space.data.name}>
            <span className="space-mark" aria-hidden="true"><SumiMark /></span>
            <strong>{space.data.name}</strong>
          </Link>
          <button
            className="icon-button space-search-button"
            type="button"
            aria-label="Jump to"
            title="Jump to (⌘K)"
            onClick={() => setSwitcherOpen(true)}
          >
            <Search />
          </button>
          <button
            className="navigation-close icon-button"
            type="button"
            aria-label="Close navigation"
            title="Close navigation"
            onClick={closeNavigation}
          >
            <X />
          </button>
        </header>
        <div className="space-navigation-body">
          <nav className="nav-group" aria-label="Space tools">
            <NavigationItem
              icon={Inbox}
              label="Inbox"
              active={active === "inbox"}
              href={`/s/${spaceSlugValue}/inbox`}
              count={inboxCount}
            />
            <NavigationItem
              icon={ListTodo}
              label="Tasks"
              active={active === "tasks"}
              href={`/s/${spaceSlugValue}/tasks`}
            />
            <NavigationItem
              icon={Users}
              label="Members"
              active={active === "members" || active === "agents"}
              href={`/s/${spaceSlugValue}/members`}
            />
            <NavigationItem
              icon={Monitor}
              label="Computers"
              active={active === "computers"}
              href={`/s/${spaceSlugValue}/computers`}
            />
            {companyOfficeEnabled ? (
              <NavigationItem
                icon={Building2}
                label="Company"
                active={active === "company"}
                href={`/s/${spaceSlugValue}/company`}
              />
            ) : null}
            {agentInsightsEnabled ? (
              <NavigationItem
                icon={Network}
                label="Agent insights"
                active={active === "insights"}
                href={`/s/${spaceSlugValue}/insights/stats`}
              />
            ) : null}
          </nav>
          <section className="nav-group" aria-labelledby="nav-channels-heading">
            <div className="nav-section-heading">
              <h2 className="nav-label" id="nav-channels-heading">Channels</h2>
              {channels.data.can_create ? (
                <button
                  type="button"
                  aria-label="Create Channel"
                  title="Create Channel"
                  onClick={() => {
                    setChannelFormOpen(true);
                    channelCreation.reset();
                  }}
                >
                  <Plus />
                </button>
              ) : null}
            </div>
            {channels.data.channels
              .filter((channel) => channel.joined)
              .map((channel) => (
                <NavigationItem
                  key={channel.id}
                  icon={channel.kind === "private" ? LockKeyhole : Hash}
                  label={channel.slug}
                  active={
                    active === "channel" &&
                    location.pathname.endsWith(`/channels/${channel.slug}`)
                  }
                  href={`/s/${spaceSlugValue}/channels/${channel.slug}`}
                  unread={unreadChannelIds.has(channel.id)}
                />
              ))}
          </section>
          {channels.data.channels.some((channel) => !channel.joined) ? (
            <section className="nav-group" aria-labelledby="nav-discover-heading">
              <div className="nav-section-heading">
                <h2 className="nav-label" id="nav-discover-heading">Discover</h2>
              </div>
              {channels.data.channels
                .filter((channel) => !channel.joined)
                .map((channel) => (
                  <div className="discover-channel" key={channel.id}>
                    <Hash aria-hidden="true" />
                    <span title={channel.topic ?? channel.slug}>{channel.slug}</span>
                    <button
                      type="button"
                      disabled={channelJoin.isPending}
                      aria-label={`Join #${channel.slug}`}
                      title={`Join #${channel.slug}`}
                      onClick={() => channelJoin.mutate(channel.id)}
                    >
                      Join
                    </button>
                  </div>
                ))}
            </section>
          ) : null}
          <section className="nav-group" aria-labelledby="nav-dms-heading">
            <div className="nav-section-heading">
              <h2 className="nav-label" id="nav-dms-heading">Direct messages</h2>
              <button
                type="button"
                aria-label="Start DM"
                title="Start DM"
                onClick={() => {
                  setDirectMessageFormOpen(true);
                  directMessageCreation.reset();
                }}
              >
                <Plus />
              </button>
            </div>
            {directMessages.error ? (
              <span className="nav-empty">DMs unavailable</span>
            ) : availableDirectMessages.length === 0 ? (
              <span className="nav-empty">No DMs yet</span>
            ) : null}
            {availableDirectMessages.map((dm) => (
              <DirectMessageNavigationItem
                key={dm.channel_id}
                member={dm.other_member}
                activityStatus={activityByMemberId.get(dm.other_member.id)}
                role={roleByMemberId.get(dm.other_member.id)}
                active={
                  active === "dm" &&
                  location.pathname.endsWith(`/dm/${dm.other_member.id}`)
                }
                href={`/s/${spaceSlugValue}/dm/${dm.other_member.id}`}
                unread={unreadChannelIds.has(dm.channel_id)}
              />
            ))}
          </section>
        </div>
        <footer className="space-self">
          <PixelIdentity name={currentMember.display_name} kind="human" seed={currentMember.id} />
          <span title={currentMember.display_name}>{currentMember.display_name}</span>
          <Link
            className={`icon-button space-settings-link${active === "settings" ? " space-settings-link--active" : ""}`}
            to="/s/$spaceSlug/settings"
            params={{ spaceSlug: spaceSlugValue }}
            aria-label="Settings"
            title="Settings"
            aria-current={active === "settings" ? "page" : undefined}
          >
            <Settings aria-hidden="true" />
          </Link>
        </footer>
      </aside>

      {channelFormOpen ? (
        <ChannelDialog
          agents={members.data.filter((member) => member.kind === "agent")}
          pending={channelCreation.isPending}
          error={channelCreation.error?.message}
          close={() => setChannelFormOpen(false)}
          onSubmit={(input) => channelCreation.mutate(input)}
        />
      ) : null}

      {switcherOpen ? (
        <QuickSwitcher
          items={switcherItems}
          close={() => setSwitcherOpen(false)}
          open={(href) => {
            setSwitcherOpen(false);
            dismissNavigationDrawer();
            void navigate({ to: href });
          }}
        />
      ) : null}

      {directMessageFormOpen ? (
        <DirectMessageDialog
          members={members.data.filter((member) => member.id !== currentMember.id)}
          activityByMemberId={activityByMemberId}
          existingMemberIds={new Set(availableDirectMessages.map((dm) => dm.other_member.id))}
          pendingMemberId={directMessageCreation.isPending ? directMessageCreation.variables : undefined}
          error={directMessageCreation.error?.message}
          close={() => setDirectMessageFormOpen(false)}
          onSelect={(memberId) => directMessageCreation.mutate(memberId)}
        />
      ) : null}

        {contextNavigation ? (
          // Section lists live beside the page so the sidebar stays put.
          <div className="space-section">
            <nav className="section-nav" aria-label="Paired computers">
              {contextNavigation}
            </nav>
            <div className="section-main">{page}</div>
          </div>
        ) : page}
      </main>
    </>
  );
}

function RouteFailure({ error, retry }: { error: unknown; retry: () => void }) {
  const message = error instanceof ApiRequestError ? error.message : "The Server did not return a usable response.";
  return <main className="route-status route-status--error"><section className="route-status-panel" role="alert"><p className="section-kicker">Could not open Space</p><h1>Something interrupted this view.</h1><p>{message}</p><button className="command-button command-button--accent" type="button" onClick={retry}>Retry</button></section></main>;
}

function ComputersNavigation({ computers, spaceSlug, activeHash, canManage }: { computers: Computer[]; spaceSlug: string; activeHash: string; canManage: boolean }) {
  const normalizedHash = activeHash.replace(/^#/, "");
  return <>
    <div className="nav-section-heading">
      <p className="nav-label">Paired computers <span>{computers.length}</span></p>
      {canManage ? <Link to="/s/$spaceSlug/computers" params={{ spaceSlug }} hash="pair-computer" aria-label="Pair Computer" title="Pair Computer"><Plus /></Link> : null}
    </div>
    {computers.map((computer) => {
      const hash = `computer-${computer.id}`;
      const selected = normalizedHash === hash;
      return <Link key={computer.id} className={`context-entity-row computer-context-row${selected ? " context-entity-row--active" : ""}`} to="/s/$spaceSlug/computers" params={{ spaceSlug }} hash={hash}>
        <Monitor aria-hidden="true" />
        <span><strong title={computer.name}>{computer.name}</strong><small title={`${computer.status} · daemon v${computer.daemon_version}`}><i className={`computer-presence computer-presence--${computer.status}`} aria-hidden="true" />{computer.status}<em>v{computer.daemon_version}</em></small></span>
      </Link>;
    })}
  </>;
}

function viewingChannelId(
  pathname: string,
  channels: Channel[] | undefined,
  directMessages: DirectMessage[] | undefined,
): string | undefined {
  const channelMatch = pathname.match(/\/channels\/([^/]+)$/);
  if (channelMatch && channels) {
    return channels.find((candidate) => candidate.slug === channelMatch[1])?.id;
  }
  const dmMatch = pathname.match(/\/dm\/([^/]+)$/);
  if (dmMatch && directMessages) {
    return directMessages.find(
      (candidate) => candidate.other_member.id === dmMatch[1],
    )?.channel_id;
  }
  return undefined;
}

function ChannelDialog({
  agents,
  pending,
  error,
  close,
  onSubmit,
}: {
  agents: Member[];
  pending: boolean;
  error?: string;
  close: () => void;
  onSubmit: (input: Parameters<typeof createChannel>[1]) => void;
}) {
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    onSubmit({
      slug: String(form.get("slug") ?? ""),
      kind: String(form.get("kind") ?? "public") as "public" | "private",
      topic: String(form.get("topic") ?? ""),
      agent_member_ids: form.getAll("agent_member_ids").map(String),
    });
  }

  return (
    <DialogFrame className="channel-dialog" close={close} labelId="create-channel-title">
        <header>
          <div><p className="section-kicker">New conversation</p><h2 id="create-channel-title">Create Channel</h2></div>
          <button className="icon-button" type="button" aria-label="Close Create Channel" onClick={close}><X /></button>
        </header>
        <form className="channel-create-form" onSubmit={submit}>
          <label>Slug<input name="slug" required maxLength={32} pattern="[a-z0-9]+(?:-[a-z0-9]+)*" data-dialog-initial-focus /></label>
          <label>Visibility<select name="kind" defaultValue="public"><option value="public">Public</option><option value="private">Private</option></select></label>
          <label>Topic<input name="topic" maxLength={200} /></label>
          <fieldset className="channel-agent-picker">
            <legend>Initial Agents</legend>
            {agents.length ? agents.map((agent) => (
              <label key={agent.id}>
                <input type="checkbox" name="agent_member_ids" value={agent.id} />
                <PixelIdentity name={agent.display_name} kind="agent" seed={agent.id} />
                <span><strong>{agent.display_name}</strong><small>Agent</small></span>
              </label>
            )) : <p>No active Agents are available.</p>}
          </fieldset>
          {error ? <p className="form-error" role="alert">{error}</p> : null}
          <footer>
            <button className="command-button" type="button" onClick={close}>Cancel</button>
            <button className="command-button command-button--accent" type="submit" disabled={pending}>
              {pending ? <LoaderCircle className="spin" aria-hidden="true" /> : null}
              Create Channel
            </button>
          </footer>
        </form>
    </DialogFrame>
  );
}

function DirectMessageDialog({
  members,
  activityByMemberId,
  existingMemberIds,
  pendingMemberId,
  error,
  close,
  onSelect,
}: {
  members: Member[];
  activityByMemberId: Map<string, Agent["activity_status"]>;
  existingMemberIds: Set<string>;
  pendingMemberId?: string;
  error?: string;
  close: () => void;
  onSelect: (memberId: string) => void;
}) {
  const [query, setQuery] = useState("");
  const normalizedQuery = query.trim().toLocaleLowerCase();
  const filteredMembers = members.filter((member) =>
    !normalizedQuery ||
    member.display_name.toLocaleLowerCase().includes(normalizedQuery),
  );

  return (
    <DialogFrame className="dm-dialog" close={close} labelId="start-dm-title">
      <header>
        <div><p className="section-kicker">Direct message</p><h2 id="start-dm-title">Start DM</h2></div>
        <button className="icon-button" type="button" aria-label="Close Start DM" onClick={close}><X /></button>
      </header>
      <div className="dm-picker">
        <label className="dm-search">Find a Member<input data-dialog-initial-focus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Name" /></label>
        <ul className="dm-member-list" aria-label="Members available for DM">
          {filteredMembers.length ? filteredMembers.map((member) => {
            const pending = pendingMemberId === member.id;
            const activityStatus = activityByMemberId.get(member.id);
            return (
              <li key={member.id}>
                <button type="button" disabled={Boolean(pendingMemberId)} onClick={() => onSelect(member.id)}>
                  <PresenceIdentity name={member.display_name} kind={member.kind} seed={member.id} activityStatus={activityStatus} />
                  <span><strong>{member.display_name}</strong><small>{member.kind === "agent" ? activityLabel(activityStatus) : "Human"}</small></span>
                  <b>{pending ? "Opening…" : existingMemberIds.has(member.id) ? "Open" : "Start"}</b>
                </button>
              </li>
            );
          }) : <li className="nav-empty">No matching Members</li>}
        </ul>
        {error ? <p className="form-error" role="alert">{error}</p> : null}
      </div>
    </DialogFrame>
  );
}

function NavigationItem({
  icon: Icon,
  label,
  active = false,
  href,
  unread = false,
  count = 0,
}: {
  icon: LucideIcon;
  label: string;
  active?: boolean;
  href?: string;
  unread?: boolean;
  count?: number;
}) {
  const content = (
    <>
      <Icon aria-hidden="true" />
      <span>{label}</span>
      {count ? <span className="nav-count" aria-label={`${count} waiting for you`}>{count}</span> : null}
      {unread ? <span className="nav-unread-dot" aria-label="Unread messages" /> : null}
    </>
  );
  const className = `nav-item${active ? " nav-item--active" : ""}${unread ? " nav-item--unread" : ""}`;
  return href ? (
    <Link className={className} to={href} aria-current={active ? "page" : undefined}>
      {content}
    </Link>
  ) : (
    <span className={`${className} nav-item--disabled`}>{content}</span>
  );
}

function DirectMessageNavigationItem({ member, activityStatus, role, active, href, unread }: { member: Member; activityStatus?: Agent["activity_status"]; role?: string; active: boolean; href: string; unread: boolean }) {
  const working = activityStatus === "working" || activityStatus === "dispatched";
  return (
    <Link className={`nav-item dm-nav-item${active ? " nav-item--active" : ""}${unread ? " nav-item--unread" : ""}`} to={href} aria-current={active ? "page" : undefined}>
      <PresenceIdentity name={member.display_name} kind={member.kind} seed={member.id} activityStatus={activityStatus} />
      <span>
        <strong title={member.display_name}>{member.display_name}</strong>
        {member.kind === "agent" ? (
          <small className={working ? "dm-nav-status--working" : undefined} title={role}>
            {working ? activityLabel(activityStatus) : role || activityLabel(activityStatus)}
          </small>
        ) : null}
      </span>
      {unread ? <span className="nav-unread-dot" aria-label="Unread messages" /> : null}
    </Link>
  );
}

export function PresenceIdentity({ name, kind = "human", seed, activityStatus }: { name: string; kind?: "human" | "agent"; seed: string; activityStatus?: Agent["activity_status"] }) {
  if (kind !== "agent" || !activityStatus) {
    return <PixelIdentity name={name} kind={kind} seed={seed} />;
  }
  const label = activityLabel(activityStatus);
  // Working breathes a teal ring around the seal; idle stays silent; only
  // error and suspended add a corner dot.
  return (
    <span className={`presence-identity presence-identity--${activityStatus}`}>
      <PixelIdentity name={name} kind={kind} seed={seed} />
      <span className={`presence-dot presence-dot--${activityStatus}`} role="img" aria-label={`${name} is ${label}`} title={`${name} · ${label}`} />
    </span>
  );
}
