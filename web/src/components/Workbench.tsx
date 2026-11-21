import { useEffect, useMemo, useRef, useState, type DragEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, CheckCircle2, ChevronRight, FolderPlus, Hash, Inbox, Lock, Monitor, Moon, Pencil, Plus, Search, Settings, Sparkles, Sun, Trash2, Users } from "lucide-react";
import { api, ownComputers, type BootstrapData, type ChannelGroup, type Conversation, type Member, type Space } from "../api";
import { accentSwatch, applyAccent } from "../accent";
import { readPendingPairing } from "../pairing";
import { useTheme } from "../theme";
import { AgentForm } from "./AgentForm";
import { agentStatusText, MemberAvatar } from "./Avatar";
import { ConversationView } from "./ConversationView";
import { DesktopServerTrigger } from "./DesktopServer";
import { DetailPane, type DetailTarget } from "./DetailPane";
import { ComputerStep, PairingCard } from "./Setup";
import { DETAIL_MIN, ResizeHandle, SIDEBAR_MAX, SIDEBAR_MIN, usePaneWidths } from "./Resize";
import { WorkbenchSkeleton } from "./Skeletons";
import { Button, Modal, useAction } from "./ui";
import { AdminPanel, InboxView, TasksView, type AdminTab, type InboxFilter, type TaskFilter } from "./Views";
import { ChannelGroupDialog, JumpDialog, NewChannelDialog, NewSpaceDialog } from "./WorkbenchDialogs";

type MainView = { kind: "conversation"; convID: string; archivedConv?: Conversation } | { kind: "inbox" } | { kind: "tasks" } | { kind: "admin"; tab: AdminTab };
type Dialog = null | "channel" | "group" | "space" | "jump" | "computer" | "agent" | "pairing";

const SSE_EVENTS = [
  "message.created",
  "message.reaction_changed",
  "task.created",
  "task.updated",
  "agent.created",
  "agent.updated",
  "agent.state_changed",
  "computer.changed",
  "conversation.created",
  "conversation.updated",
  "channel_groups.changed",
  "member.permissions_changed",
  "conversation.members_changed",
  "space.updated",
];

/** Remount (and replay the slide-in) only when the panel shows a different object. */
function detailKey(d: DetailTarget) {
  return d.kind === "task"
    ? `t:${d.taskID}`
    : d.kind === "thread"
      ? `th:${d.threadConvID}`
      : d.kind === "agent"
        ? `a:${d.memberID}`
        : d.kind === "human"
          ? `h:${d.memberID}`
          : `m:${d.convID}`;
}

function NavItem({
  active,
  onClick,
  icon,
  children,
  trailing,
  dim,
  bold,
}: {
  active: boolean;
  onClick: () => void;
  icon: ReactNode;
  children: ReactNode;
  trailing?: ReactNode;
  dim?: boolean;
  bold?: boolean;
}) {
  return (
    <button
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      className={`w-full h-8 px-2 rounded-[8px] flex items-center gap-2.5 text-[14px] transition-colors duration-100 ${
        active ? "bg-nav-active text-nav-fg-strong" : dim ? "text-nav-fg-3 hover:bg-nav-hover" : bold ? "text-nav-fg-strong font-semibold hover:bg-nav-hover" : "text-nav-fg-2 hover:bg-nav-hover hover:text-nav-fg"
      }`}
    >
      <span className="size-5 flex items-center justify-center shrink-0 [&>svg]:size-4">{icon}</span>
      <span className="truncate flex-1 text-left">{children}</span>
      {trailing}
    </button>
  );
}

function Count({ n, tone = "warm" }: { n: number; tone?: "warm" | "muted" }) {
  return (
    <span
      className={`min-w-5 h-5 px-1.5 rounded-full text-[11.5px] font-semibold tabular inline-flex items-center justify-center animate-pop-in ${
        tone === "warm" ? "bg-accent text-accent-fg" : "bg-nav-fill-2 text-nav-fg-2"
      }`}
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

function SectionLabel({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="px-2 mb-1 h-7 flex items-center justify-between">
      <span className="text-[12px] font-semibold uppercase tracking-[0.06em] text-nav-fg-3">{children}</span>
      {action}
    </div>
  );
}

function SectionAdd({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button onClick={onClick} title={label} aria-label={label} className="size-6 rounded-[6px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center">
      <Plus className="size-4" />
    </button>
  );
}

export function Workbench({
  spaceSlug,
  spaces = [],
  onSwitchSpace,
  onSpaceCreated,
  initialPeerID,
  onLogout,
}: {
  spaceSlug: string;
  spaces?: Space[];
  onSwitchSpace?: (slug: string) => void;
  onSpaceCreated?: (slug: string) => void;
  initialPeerID?: string;
  onLogout: () => void;
}) {
  const qc = useQueryClient();
  const bootQ = useQuery({ queryKey: ["bootstrap", spaceSlug], queryFn: () => api.bootstrap(spaceSlug) });
  const [theme, setTheme] = useTheme();
  const [mainView, setMainView] = useState<MainView>({ kind: "inbox" });
  const [detail, setDetail] = useState<DetailTarget | null>(null);
  const [inboxFilter, setInboxFilter] = useState<InboxFilter>("needs_me");
  const [taskFilter, setTaskFilter] = useState<TaskFilter>("active");
  const [dialog, setDialog] = useState<Dialog>(() => (readPendingPairing() ? "pairing" : null));
  const [editingGroup, setEditingGroup] = useState<ChannelGroup | null>(null);
  const [dragChannelID, setDragChannelID] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [reconnecting, setReconnecting] = useState(false);
  const dm = useAction();
  const groupAct = useAction();
  const landed = useRef(false);
  const boot = bootQ.data;
  const panes = usePaneWidths();
  const accent = boot?.space.accent;
  useEffect(() => applyAccent(accent), [accent]);

  // First landing: open the agent created during onboarding, else #general when Inbox is empty.
  useEffect(() => {
    if (!boot || landed.current) return;
    landed.current = true;
    if (initialPeerID && boot.members.some((m) => m.id === initialPeerID)) {
      void openOrCreateDM(initialPeerID);
      return;
    }
    const firstChannel = boot.conversations.find((c) => c.kind === "public" || c.kind === "private");
    if (boot.inbox.length === 0 && firstChannel) setMainView({ kind: "conversation", convID: firstChannel.id });
  }, [boot]);

  useEffect(() => {
    const es = new EventSource(`/api/v1/spaces/${spaceSlug}/events`, { withCredentials: true });
    let dropped = false;
    let stateTimer: number | null = null;

    const invalidateAll = () => {
      for (const k of ["bootstrap", "inbox", "tasks", "messages", "task", "conv-members", "archived-channels"]) {
        void qc.invalidateQueries({ queryKey: [k, spaceSlug] });
      }
    };

    const parsePayload = (raw: Event): Record<string, unknown> => {
      try {
        const data = (raw as MessageEvent).data;
        return typeof data === "string" && data ? (JSON.parse(data) as Record<string, unknown>) : {};
      } catch {
        return {};
      }
    };

    const handleEvent = (kind: string) => (raw: Event) => {
      const p = parsePayload(raw);
      const convID = typeof p.conversation_id === "string" ? p.conversation_id : "";
      const taskID = typeof p.task_id === "string" ? p.task_id : "";

      switch (kind) {
        case "agent.state_changed":
          // Coalesce rapid tool-activity ticks so chat scroll and typing stay silky smooth.
          if (stateTimer === null) {
            stateTimer = window.setTimeout(() => {
              stateTimer = null;
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
            }, 250);
          }
          return;
        case "message.created":
        case "message.reaction_changed":
          if (convID) void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, convID] });
          else void qc.invalidateQueries({ queryKey: ["messages", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
          return;
        case "task.created":
        case "task.updated":
          void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
          if (taskID) void qc.invalidateQueries({ queryKey: ["task", spaceSlug, taskID] });
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
          return;
        case "conversation.members_changed":
          if (convID) void qc.invalidateQueries({ queryKey: ["conv-members", spaceSlug, convID] });
          else void qc.invalidateQueries({ queryKey: ["conv-members", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          return;
        case "conversation.created":
        case "conversation.updated":
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["archived-channels", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
          return;
        case "space.updated":
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
          void qc.invalidateQueries({ queryKey: ["me"] });
          return;
        default:
          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      }
    };

    es.onopen = () => {
      if (dropped) {
        dropped = false;
        setReconnecting(false);
        invalidateAll();
      }
    };
    es.onerror = () => {
      dropped = true;
      setReconnecting(true);
    };
    for (const ev of SSE_EVENTS) es.addEventListener(ev, handleEvent(ev));
    return () => {
      if (stateTimer !== null) window.clearTimeout(stateTimer);
      setReconnecting(false);
      es.close();
    };
  }, [spaceSlug, qc]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setDialog((d) => (d === "jump" ? null : "jump"));
        return;
      }
      if (e.key === "Escape" && !dialog && detail) {
        const tag = (document.activeElement as HTMLElement | null)?.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || document.querySelector('[role="dialog"],[role="listbox"],[role="menu"]')) return;
        setDetail(null);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [dialog, detail]);

  const activeConvID = mainView.kind === "conversation" ? mainView.convID : "";
  const activeConv = useMemo(
    () =>
      boot?.conversations.find((c) => c.id === activeConvID) ??
      (mainView.kind === "conversation" ? mainView.archivedConv : undefined),
    [boot, activeConvID, mainView],
  );

  async function openOrCreateDM(peerMemberID: string) {
    const existing = qc
      .getQueryData<typeof boot>(["bootstrap", spaceSlug])
      ?.conversations.find((c) => c.kind === "dm" && c.peer_member_id === peerMemberID);
    if (existing) {
      setMainView({ kind: "conversation", convID: existing.id });
      return;
    }
    await dm.run(async () => {
      const res = await api.createDM(spaceSlug, peerMemberID);
      await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      setMainView({ kind: "conversation", convID: res.conversation.id });
    });
  }

  if (!boot) return <WorkbenchSkeleton />;

  const channels = boot.conversations.filter((c) => c.kind === "public" || c.kind === "private");
  const channelGroups = boot.channel_groups ?? [];
  const channelByID = new Map(channels.map((c) => [c.id, c]));
  const groupedConvIDs = new Set(channelGroups.flatMap((g) => g.conversation_ids));
  const ungroupedChannels = channels.filter((c) => !groupedConvIDs.has(c.id));
  const dms = boot.conversations.filter((c) => c.kind === "dm");
  const agents = boot.members.filter((m) => m.kind === "agent");
  const people = boot.members.filter((m) => m.kind === "human" && m.id !== boot.current_member.id);
  const onlineComputers = boot.computers.filter((c) => c.connection_status === "online").length;
  const canGovern = boot.current_member.access_level !== "member";
  const myComputers = ownComputers(boot);
  const needsSetup = canGovern && (myComputers.length === 0 || agents.length === 0);
  const needsMe = boot.inbox.filter((i) => i.needs_attention).length;
  const pairing = readPendingPairing();
  const dark = theme === "dark" || (theme === "system" && typeof window.matchMedia === "function" && window.matchMedia("(prefers-color-scheme: dark)").matches);

  function handleChannelDragStart(e: DragEvent, convID: string) {
    setDragChannelID(convID);
    e.dataTransfer.setData("text/x-sumi-channel", convID);
    e.dataTransfer.effectAllowed = "move";
  }

  function handleChannelDragEnd() {
    setDragChannelID(null);
    setDropTarget(null);
  }

  function handleGroupDragOver(e: DragEvent, targetKey: string) {
    if (!dragChannelID && !e.dataTransfer.types.includes("text/x-sumi-channel")) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    if (dropTarget !== targetKey) setDropTarget(targetKey);
  }

  function handleGroupDrop(e: DragEvent, targetGroupID: string) {
    e.preventDefault();
    const convID = e.dataTransfer.getData("text/x-sumi-channel") || dragChannelID;
    setDragChannelID(null);
    setDropTarget(null);
    if (!convID) return;
    const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
    if (prev) {
      qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
        ...prev,
        channel_groups: (prev.channel_groups ?? []).map((g) => {
          const without = g.conversation_ids.filter((id) => id !== convID);
          return g.id === targetGroupID ? { ...g, conversation_ids: [...without, convID] } : { ...g, conversation_ids: without };
        }),
      });
    }
    void groupAct.run(async () => {
      try {
        await api.setConversationGroup(spaceSlug, convID, targetGroupID);
      } catch (err) {
        if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
        throw err;
      }
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

  function toggleGroupCollapse(g: ChannelGroup) {
    const nextCollapsed = !g.collapsed;
    const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
    if (prev) {
      qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
        ...prev,
        channel_groups: (prev.channel_groups ?? []).map((item) => (item.id === g.id ? { ...item, collapsed: nextCollapsed } : item)),
      });
    }
    void groupAct.run(async () => {
      try {
        await api.updateChannelGroup(spaceSlug, g.id, { collapsed: nextCollapsed });
      } catch (err) {
        if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
        throw err;
      }
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

  const renderChannelRow = (c: Conversation) => {
    const muted = c.notify_level === "mute";
    return (
      <div
        key={c.id}
        draggable
        onDragStart={(e) => handleChannelDragStart(e, c.id)}
        onDragEnd={handleChannelDragEnd}
        className={dragChannelID === c.id ? "opacity-45" : ""}
      >
        <NavItem
          active={activeConvID === c.id}
          onClick={() => setMainView({ kind: "conversation", convID: c.id })}
          icon={c.kind === "private" ? <Lock /> : <Hash />}
          dim={muted}
          bold={c.unread_count > 0 && !muted}
          trailing={
            c.mention_count > 0 ? <Count n={c.mention_count} /> : c.unread_count > 0 && !muted ? <span className="size-2 rounded-full bg-nav-fg-strong shrink-0 mr-1.5" /> : null
          }
        >
          {c.slug}
        </NavItem>
      </div>
    );
  };

  const memberRow = (m: Member) => {
    const conv = dms.find((d) => d.peer_member_id === m.id);
    const active = Boolean(conv && activeConvID === conv.id);
    const st = m.agent?.runtime_state;
    const statusSub = m.kind === "agent" && (st === "working" || st === "error") ? agentStatusText(boot, m) : "";
    const roleSub = m.kind === "agent" && !statusSub ? (m.agent?.role_text ?? "").replace(/\s+/g, " ").trim() : "";
    const hasSub = Boolean(statusSub || roleSub);
    return (
      <button
        key={m.id}
        onClick={() => void openOrCreateDM(m.id)}
        aria-current={active ? "page" : undefined}
        className={`w-full px-2 ${hasSub ? "py-1.5" : "h-9"} rounded-[8px] flex items-center gap-2.5 text-left transition-colors duration-100 ${
          active ? "bg-nav-active" : "hover:bg-nav-hover"
        }`}
      >
        <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" state={m.kind === "agent" ? st : undefined} ring="var(--nav)" />
        <span className="min-w-0 flex-1">
          <span className={`block truncate text-[14px] leading-5 ${active || (conv && conv.unread_count > 0) ? "text-nav-fg-strong font-semibold" : "text-nav-fg-2"}`}>{m.display_name}</span>
          {statusSub ? (
            <span className={`block truncate text-[12px] leading-4 ${st === "error" ? "text-nav-danger" : "text-nav-busy"}`}>{statusSub}</span>
          ) : roleSub ? (
            <span className="block fade-truncate text-[11.5px] leading-4 text-nav-fg-3" title={roleSub}>
              {roleSub}
            </span>
          ) : null}
        </span>
        {conv && conv.unread_count > 0 && <Count n={conv.unread_count} />}
      </button>
    );
  };

  const railBtn = (tab: AdminTab, icon: ReactNode, title: string, dot?: boolean) => {
    const active = mainView.kind === "admin" && mainView.tab === tab;
    return (
      <button
        key={tab}
        onClick={() => setMainView({ kind: "admin", tab })}
        title={title}
        aria-label={title}
        className={`relative size-9 rounded-[10px] flex items-center justify-center transition-colors [&>svg]:size-[18px] ${
          active ? "bg-nav-active text-nav-fg-strong" : "text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong"
        }`}
      >
        {icon}
        {dot !== undefined && (
          <span className={`absolute top-1.5 right-1.5 size-2 rounded-full ring-2 ring-[var(--nav-rail)] ${dot ? "bg-online" : "bg-nav-fg-3"}`} />
        )}
      </button>
    );
  };

  return (
    <div
      className="h-full grid bg-bg"
      style={{ gridTemplateColumns: `56px ${panes.sidebar}px minmax(0, 1fr) ${detail ? panes.detail : 0}px` }}
    >
      {/* Rail */}
      <nav aria-label="Spaces" className="bg-nav-rail titlebar-pad flex flex-col items-center justify-between pb-3 select-none">
        <div className="flex flex-col items-center gap-2 pt-3">
          {(spaces.length > 0 ? spaces : [boot.space]).map((sp) => {
            const active = sp.slug === spaceSlug;
            return (
              <button
                key={sp.id}
                onClick={() => (active ? setMainView({ kind: "inbox" }) : onSwitchSpace?.(sp.slug))}
                title={sp.name}
                aria-label={`Space ${sp.name}`}
                className="relative size-9 flex items-center justify-center group"
              >
                <span
                  className={`absolute -left-3 w-1 rounded-r-full bg-nav-fg-strong transition-all duration-200 ${active ? "h-6 opacity-100" : "h-2 opacity-0 group-hover:opacity-60"}`}
                />
                <span
                  className={`size-9 rounded-[11px] text-[14px] font-semibold flex items-center justify-center transition-[background-color,border-radius,opacity] ${
                    active ? "text-white shadow-sm" : "opacity-55 hover:opacity-90 hover:rounded-[9px] text-white"
                  }`}
                  style={{ background: accentSwatch(sp.slug === spaceSlug ? boot.space.accent : sp.accent) }}
                >
                  {(sp.name[0] ?? "S").toUpperCase()}
                </span>
              </button>
            );
          })}
          <button
            onClick={() => setDialog("space")}
            title="New space"
            aria-label="New space"
            className="size-9 rounded-[11px] border border-dashed border-nav-line-strong text-nav-fg-3 hover:text-nav-fg-strong hover:border-nav-fg-3 flex items-center justify-center transition-colors"
          >
            <Plus className="size-4" />
          </button>
        </div>
        <div className="flex flex-col items-center gap-1">
          {railBtn("agents", <Bot />, "Agents")}
          {railBtn("computers", <Monitor />, `Computers · ${onlineComputers} online`, onlineComputers > 0)}
          {railBtn("people", <Users />, "People")}
          {railBtn("settings", <Settings />, "Settings")}
          <DesktopServerTrigger variant="rail" />
        </div>
      </nav>

      {/* Sidebar */}
      <aside aria-label="Sidebar" className="relative bg-nav border-r border-nav-line flex flex-col min-h-0 select-none">
        <ResizeHandle
          label="Resize sidebar"
          edge="right"
          value={panes.sidebar}
          min={SIDEBAR_MIN}
          max={SIDEBAR_MAX}
          onResize={panes.setSidebar}
          onReset={panes.resetSidebar}
        />
        <div className="h-[52px] px-4 flex items-center shrink-0">
          <span className="text-[15px] font-semibold text-nav-fg-strong truncate">{boot.space.name}</span>
        </div>
        <div className="px-3 pb-2 shrink-0">
          <button
            onClick={() => setDialog("jump")}
            className="w-full h-9 px-3 rounded-[9px] bg-nav-fill hover:bg-nav-fill-2 border border-nav-line text-nav-fg-3 flex items-center gap-2 text-[13.5px] transition-colors"
          >
            <Search className="size-4" />
            <span className="flex-1 text-left">Jump to…</span>
            <kbd className="text-[11.5px] font-sans px-1.5 h-5 rounded-[5px] bg-nav-fill-2 inline-flex items-center">⌘K</kbd>
          </button>
        </div>

        <div className="flex-1 min-h-0 px-3 pb-3 space-y-5 overflow-y-auto scroll-nav">
          <div className="space-y-0.5 pt-1">
            <NavItem
              active={mainView.kind === "inbox"}
              onClick={() => setMainView({ kind: "inbox" })}
              icon={<Inbox />}
              trailing={needsMe > 0 ? <Count n={needsMe} /> : boot.inbox.length > 0 ? <Count n={boot.inbox.length} tone="muted" /> : null}
            >
              Inbox
            </NavItem>
            <NavItem
              active={mainView.kind === "tasks"}
              onClick={() => setMainView({ kind: "tasks" })}
              icon={<CheckCircle2 />}
              trailing={boot.tasks.length > 0 ? <Count n={boot.tasks.length} tone="muted" /> : null}
            >
              Tasks
            </NavItem>
          </div>

          <div
            onDragOver={(e) => handleGroupDragOver(e, "__ungrouped__")}
            onDragLeave={(e) => {
              if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropTarget((cur) => (cur === "__ungrouped__" ? null : cur));
            }}
            onDrop={(e) => handleGroupDrop(e, "")}
            className={`rounded-[10px] transition-colors ${dropTarget === "__ungrouped__" ? "ring-1 ring-accent bg-accent-soft/30" : ""}`}
          >
            <SectionLabel
              action={
                <div className="flex items-center gap-0.5">
                  <button
                    onClick={() => {
                      setEditingGroup(null);
                      setDialog("group");
                    }}
                    title="New channel group"
                    aria-label="New channel group"
                    className="size-6 rounded-[6px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center"
                  >
                    <FolderPlus className="size-3.5" />
                  </button>
                  <SectionAdd label="New channel" onClick={() => setDialog("channel")} />
                </div>
              }
            >
              Channels
            </SectionLabel>
            <div className="space-y-0.5">
              {ungroupedChannels.map(renderChannelRow)}
              {ungroupedChannels.length === 0 && dragChannelID && (
                <div className="px-2 py-2 text-[12px] text-nav-fg-3 text-center rounded-[8px] border border-dashed border-nav-line-strong">
                  Drop here to ungroup
                </div>
              )}
            </div>
          </div>

          {channelGroups.map((g) => {
            const items = g.conversation_ids.map((id) => channelByID.get(id)).filter((c): c is Conversation => Boolean(c));
            const groupMentions = items.reduce((sum, c) => sum + c.mention_count, 0);
            const groupHasUnread = items.some((c) => c.unread_count > 0 && c.notify_level !== "mute");
            const isOver = dropTarget === g.id;
            return (
              <div
                key={g.id}
                onDragOver={(e) => handleGroupDragOver(e, g.id)}
                onDragLeave={(e) => {
                  if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropTarget((cur) => (cur === g.id ? null : cur));
                }}
                onDrop={(e) => handleGroupDrop(e, g.id)}
                className={`rounded-[10px] transition-colors ${isOver ? "ring-1 ring-accent bg-accent-soft/30" : ""}`}
              >
                <div className="group/grp px-2 mb-0.5 h-7 flex items-center gap-1">
                  <button
                    type="button"
                    onClick={() => toggleGroupCollapse(g)}
                    aria-expanded={!g.collapsed}
                    className="min-w-0 flex-1 flex items-center gap-1 text-left text-[12px] font-semibold uppercase tracking-[0.05em] text-nav-fg-3 hover:text-nav-fg transition-colors"
                  >
                    <ChevronRight className={`size-3.5 shrink-0 transition-transform duration-150 ${g.collapsed ? "" : "rotate-90"}`} />
                    <span className="truncate">{g.name}</span>
                    {g.collapsed && groupMentions > 0 && <Count n={groupMentions} />}
                    {g.collapsed && groupMentions === 0 && groupHasUnread && <span className="size-1.5 rounded-full bg-nav-fg-strong shrink-0 ml-1" />}
                  </button>
                  <div className="flex items-center gap-0.5 opacity-0 group-hover/grp:opacity-100 focus-within:opacity-100 transition-opacity">
                    <button
                      type="button"
                      title={`Rename ${g.name}`}
                      aria-label={`Rename ${g.name}`}
                      onClick={() => {
                        setEditingGroup(g);
                        setDialog("group");
                      }}
                      className="size-5 rounded-[5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center"
                    >
                      <Pencil className="size-3" />
                    </button>
                    <button
                      type="button"
                      title={`Delete ${g.name}`}
                      aria-label={`Delete ${g.name}`}
                      onClick={() =>
                        void groupAct.run(async () => {
                          await api.deleteChannelGroup(spaceSlug, g.id);
                          await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                        })
                      }
                      className="size-5 rounded-[5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-danger flex items-center justify-center"
                    >
                      <Trash2 className="size-3" />
                    </button>
                  </div>
                </div>
                {!g.collapsed && (
                  <div className="space-y-0.5">
                    {items.length > 0 ? (
                      items.map(renderChannelRow)
                    ) : (
                      <div className="px-2.5 py-1.5 text-[12px] text-nav-fg-3">Drag channels here</div>
                    )}
                  </div>
                )}
              </div>
            );
          })}

          <div>
            <SectionLabel action={canGovern ? <SectionAdd label="New agent" onClick={() => setDialog(myComputers.length ? "agent" : "computer")} /> : undefined}>
              Agents
            </SectionLabel>
            <div className="space-y-0.5">
              {agents.map(memberRow)}
              {agents.length === 0 && (
                <button
                  onClick={() => setDialog(myComputers.length ? "agent" : "computer")}
                  className="w-full h-9 px-2 rounded-[8px] flex items-center gap-2.5 text-[13.5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg"
                >
                  <span className="size-6 rounded-[6px] border border-dashed border-nav-line-strong flex items-center justify-center">
                    <Plus className="size-3.5" />
                  </span>
                  Create your first agent
                </button>
              )}
            </div>
          </div>

          {people.length > 0 && (
            <div>
              <SectionLabel>People</SectionLabel>
              <div className="space-y-0.5">{people.map(memberRow)}</div>
            </div>
          )}
        </div>

        {needsSetup && (
          <div className="px-3 pb-3 shrink-0 animate-fade-in">
            <button
              onClick={() => setDialog(myComputers.length === 0 ? "computer" : "agent")}
              className="w-full p-3 rounded-[12px] bg-gradient-to-br from-accent/30 to-accent/10 border border-accent/30 hover:border-accent/60 text-left transition-colors"
            >
              <div className="flex items-center gap-2 text-[14px] font-semibold text-nav-fg-strong">
                <Sparkles className="size-4" /> Finish setup
              </div>
              <div className="mt-2 space-y-1.5 text-[13px]">
                <SetupLine done={myComputers.length > 0}>Connect a computer</SetupLine>
                <SetupLine done={agents.length > 0}>Create an agent</SetupLine>
              </div>
            </button>
          </div>
        )}

        {reconnecting && (
          <div role="status" className="mx-3 mb-2 px-2.5 h-8 rounded-[8px] bg-busy-soft text-nav-busy flex items-center gap-2 text-[12.5px] font-medium shrink-0 animate-fade-in">
            <span className="size-2 rounded-full bg-busy animate-breathe shrink-0" />
            <span>Reconnecting…</span>
          </div>
        )}

        <div className="h-14 px-3 border-t border-nav-line flex items-center gap-2.5 shrink-0">
          <button onClick={() => setMainView({ kind: "admin", tab: "settings" })} className="flex items-center gap-2.5 min-w-0 flex-1 rounded-[8px] p-1 -m-1 hover:bg-nav-hover">
            <MemberAvatar id={boot.current_member.id} name={boot.current_member.display_name} kind="human" size="sm" />
            <span className="min-w-0 text-left">
              <span className="block text-[14px] leading-5 font-medium text-nav-fg-strong truncate">{boot.current_member.display_name}</span>
              <span className="block text-[12px] leading-4 text-nav-fg-3 capitalize">{boot.current_member.access_level}</span>
            </span>
          </button>
          <button
            onClick={() => setTheme(dark ? "light" : "dark")}
            aria-label={dark ? "Switch to light mode" : "Switch to dark mode"}
            title={dark ? "Light mode" : "Dark mode"}
            className="size-8 rounded-[8px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center"
          >
            {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
          </button>
        </div>
      </aside>

      {/* Main */}
      <main className="bg-panel flex flex-col min-w-0 min-h-0">
        <div className="h-full min-h-0">
          {mainView.kind === "conversation" && activeConv && (
            <ConversationView
              spaceSlug={spaceSlug}
              boot={boot}
              conv={activeConv}
              onOpenDetail={setDetail}
              onArchived={(archived) => setMainView({ kind: "conversation", convID: archived.id, archivedConv: archived })}
              onUnarchived={(restored) => setMainView({ kind: "conversation", convID: restored.id })}
            />
          )}
          {mainView.kind === "inbox" && (
            <InboxView
              spaceSlug={spaceSlug}
              filter={inboxFilter}
              onFilter={setInboxFilter}
              onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
              onOpenDetail={setDetail}
            />
          )}
          {mainView.kind === "tasks" && <TasksView spaceSlug={spaceSlug} boot={boot} filter={taskFilter} onFilter={setTaskFilter} onOpenDetail={setDetail} />}
          {mainView.kind === "admin" && (
            <AdminPanel
              boot={boot}
              tab={mainView.tab}
              onTab={(tab) => setMainView({ kind: "admin", tab })}
              onInspectAgent={(id) => setDetail({ kind: "agent", memberID: id })}
              selectedAgentID={detail?.kind === "agent" ? detail.memberID : undefined}
              onInspectHuman={(id) => setDetail({ kind: "human", memberID: id })}
              selectedHumanID={detail?.kind === "human" ? detail.memberID : undefined}
              onNewAgent={() => setDialog(myComputers.length ? "agent" : "computer")}
              onConnectComputer={() => setDialog("computer")}
              onOpenArchivedChannel={(conv) => setMainView({ kind: "conversation", convID: conv.id, archivedConv: conv })}
              theme={theme}
              onTheme={setTheme}
              onLogout={onLogout}
            />
          )}
        </div>
      </main>

      {/* Detail */}
      <div className="relative min-w-0 min-h-0 bg-panel">
        {detail && (
          <ResizeHandle
            label="Resize panel"
            edge="left"
            value={panes.detail}
            min={DETAIL_MIN}
            max={panes.detailMax}
            onResize={panes.setDetail}
            onReset={panes.resetDetail}
          />
        )}
        {detail && (
          <div key={detailKey(detail)} className="h-full overflow-hidden animate-slide-in" style={{ width: panes.detail }}>
            <DetailPane
              spaceSlug={spaceSlug}
              boot={boot}
              target={detail}
              onClose={() => setDetail(null)}
              onSelectTarget={setDetail}
              onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
              onOpenDM={(memberID) => void openOrCreateDM(memberID)}
            />
          </div>
        )}
      </div>

      <NewChannelDialog
        open={dialog === "channel"}
        spaceSlug={spaceSlug}
        members={boot.members}
        currentMemberID={boot.current_member.id}
        onClose={() => setDialog(null)}
        onCreated={(id) => {
          setDialog(null);
          setMainView({ kind: "conversation", convID: id });
        }}
      />
      <ChannelGroupDialog
        open={dialog === "group"}
        spaceSlug={spaceSlug}
        group={editingGroup}
        onClose={() => {
          setDialog(null);
          setEditingGroup(null);
        }}
      />
      <NewSpaceDialog
        open={dialog === "space"}
        onClose={() => setDialog(null)}
        onCreated={(slug) => {
          setDialog(null);
          onSpaceCreated?.(slug);
        }}
      />
      <Modal open={dialog === "computer"} onClose={() => setDialog(null)} label="Connect a computer" width={520}>
        <div className="p-6">
          <ComputerStep spaceSlug={spaceSlug} nextLabel={agents.length === 0 ? "Next: create an agent" : "Done"} onNext={() => setDialog(agents.length === 0 ? "agent" : null)} />
        </div>
      </Modal>
      <Modal open={dialog === "agent"} onClose={() => setDialog(null)} label="New agent" title="New agent" description="A persistent teammate with its own role, memory and workspace." width={540}>
        <div className="px-6 pb-6 pt-4">
          <AgentForm
            spaceSlug={spaceSlug}
            computers={myComputers}
            onCreated={(agent) => {
              setDialog(null);
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] }).then(() => openOrCreateDM(agent.id));
            }}
            footer={(submit) => (
              <div className="flex justify-end gap-2 pt-1">
                <Button type="button" variant="ghost" onClick={() => setDialog(null)}>
                  Cancel
                </Button>
                {submit}
              </div>
            )}
          />
        </div>
      </Modal>
      <Modal
        open={dialog === "pairing" && Boolean(pairing)}
        onClose={() => setDialog(null)}
        label="Connect this computer"
        title="Connect this computer to your account?"
        description="The Sumi desktop app on this machine asked to join. You can run agents from any of your spaces on it."
        width={500}
      >
        <div className="px-6 pb-6 pt-4">
          {pairing && <PairingCard spaceSlug={spaceSlug} pairing={pairing} onConfirmed={() => setDialog(null)} onDismiss={() => setDialog(null)} />}
        </div>
      </Modal>
      <JumpDialog
        open={dialog === "jump"}
        onClose={() => setDialog(null)}
        items={[
          { key: "inbox", label: "Inbox", group: "Go to", icon: <Inbox className="size-4" />, run: () => setMainView({ kind: "inbox" }) },
          { key: "tasks", label: "Tasks", group: "Go to", icon: <CheckCircle2 className="size-4" />, run: () => setMainView({ kind: "tasks" }) },
          { key: "settings", label: "Settings", group: "Go to", icon: <Settings className="size-4" />, run: () => setMainView({ kind: "admin", tab: "settings" }) },
          ...channels.map((c) => ({
            key: c.id,
            label: `#${c.slug ?? ""}`,
            keywords: c.topic ?? "",
            group: "Channels",
            icon: c.kind === "private" ? <Lock className="size-4" /> : <Hash className="size-4" />,
            run: () => setMainView({ kind: "conversation", convID: c.id }),
          })),
          ...[...agents, ...people].map((m) => ({
            key: m.id,
            label: m.display_name,
            keywords: m.agent?.role_text ?? "",
            group: m.kind === "agent" ? "Agents" : "People",
            icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" />,
            run: () => void openOrCreateDM(m.id),
          })),
          ...boot.tasks.map((t) => ({
            key: t.id,
            label: `#${t.number} ${t.title}`,
            keywords: t.channel_slug ?? "",
            group: "Tasks",
            icon: <CheckCircle2 className="size-4" />,
            run: () => setDetail({ kind: "task", taskID: t.id }),
          })),
          { key: "act:channel", label: "New channel", group: "Actions", icon: <Plus className="size-4" />, run: () => setDialog("channel") },
          {
            key: "act:group",
            label: "New channel group",
            group: "Actions",
            icon: <FolderPlus className="size-4" />,
            run: () => {
              setEditingGroup(null);
              setDialog("group");
            },
          },
          {
            key: "act:theme",
            label: dark ? "Switch to light mode" : "Switch to dark mode",
            keywords: "theme appearance dark light",
            group: "Actions",
            icon: dark ? <Sun className="size-4" /> : <Moon className="size-4" />,
            run: () => setTheme(dark ? "light" : "dark"),
          },
        ]}
      />
    </div>
  );
}

function SetupLine({ done, children }: { done: boolean; children: ReactNode }) {
  return (
    <div className={`flex items-center gap-2 ${done ? "text-nav-fg-3 line-through" : "text-nav-fg"}`}>
      <span className={`size-4 rounded-full flex items-center justify-center shrink-0 ${done ? "bg-work text-accent-fg" : "border border-nav-line-strong"}`}>
        {done && <svg viewBox="0 0 12 12" className="size-2.5" fill="none" stroke="currentColor" strokeWidth="2"><path d="M2.5 6.2l2.2 2.2 4.8-4.8" /></svg>}
      </span>
      {children}
    </div>
  );
}
