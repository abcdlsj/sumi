import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type MouseEvent as ReactMouseEvent, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowDownUp,
  AtSign,
  Bell,
  BellOff,
  Bot,
  Check,
  CheckCircle2,
  ChevronRight,
  Clock,
  Copy,
  FolderInput,
  FolderPlus,
  Hash,
  Inbox,
  Keyboard,
  Lock,
  Monitor,
  Moon,
  PanelLeft,
  Pencil,
  Plus,
  Search,
  Settings,
  Sparkles,
  Sun,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { api, ownComputers, type BootstrapData, type ChannelGroup, type Conversation, type Member, type NotifyLevel, type SearchHit, type Space } from "../api";
import { accentSwatch, applyAccent } from "../accent";
import { useDraftConvIDs } from "../drafts";
import { readPendingPairing } from "../pairing";
import { isMacPlatform, useChannelSort, useSidebarCollapsed, type ChannelSortPref } from "../preferences";
import { isDesktopShell } from "../shell";
import { useTheme } from "../theme";
import { AgentForm } from "./AgentForm";
import { WorkingNowSummary } from "./AgentVisibility";
import { agentStatusText, MemberAvatar } from "./Avatar";
import { ConversationView } from "./ConversationView";
import { DesktopServerTrigger } from "./DesktopServer";
import { DetailPane, type DetailTarget } from "./DetailPane";
import { ComputerStep, PairingCard } from "./Setup";
import { DETAIL_MIN, ResizeHandle, SIDEBAR_MAX, SIDEBAR_MIN, usePaneWidths } from "./Resize";
import { WorkbenchSkeleton } from "./Skeletons";
import { Button, ErrorBoundary, ErrorState, Menu, MenuItem, Modal, useAction, useToast } from "./ui";
import { AdminPanel, InboxView, TasksView, type AdminTab, type InboxFilter, type TaskFilter } from "./Views";
import { ChannelGroupDialog, JumpDialog, NewChannelDialog, NewSpaceDialog, ShortcutsDialog, type JumpItem } from "./WorkbenchDialogs";

type MainView =
  | { kind: "conversation"; convID: string; archivedConv?: Conversation; focusMessageID?: string }
  | { kind: "inbox" }
  | { kind: "tasks" }
  | { kind: "admin"; tab: AdminTab };
type Dialog = null | "channel" | "group" | "space" | "jump" | "shortcuts" | "computer" | "agent" | "pairing";

function readRecentConvs(spaceSlug: string): string[] {
  try {
    const raw = localStorage.getItem(`sumi.recent.${spaceSlug}`);
    const parsed = raw ? (JSON.parse(raw) as unknown) : [];
    return Array.isArray(parsed) ? parsed.filter((x): x is string => typeof x === "string").slice(0, 6) : [];
  } catch {
    return [];
  }
}

function recordRecentConv(spaceSlug: string, convID: string): string[] {
  const prev = readRecentConvs(spaceSlug);
  const next = [convID, ...prev.filter((id) => id !== convID)].slice(0, 6);
  try {
    localStorage.setItem(`sumi.recent.${spaceSlug}`, JSON.stringify(next));
  } catch {
    // ignore storage failures
  }
  return next;
}

function sortChannelsList(list: Conversation[], mode: ChannelSortPref): Conversation[] {
  if (mode === "default") return list;
  const copy = list.slice();
  if (mode === "name") {
    return copy.sort((a, b) => (a.slug ?? "").localeCompare(b.slug ?? ""));
  }
  return copy.sort((a, b) => {
    const score = (c: Conversation) => {
      if (c.mention_count > 0) return 3;
      if (c.unread_count > 0 && c.notify_level !== "mute") return 2;
      if (c.notify_level === "mute") return 0;
      return 1;
    };
    const diff = score(b) - score(a);
    return diff !== 0 ? diff : (a.slug ?? "").localeCompare(b.slug ?? "");
  });
}

const SSE_EVENTS = [
  "message.created",
  "message.reaction_changed",
  "conversation.read_updated",
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
          : d.kind === "channel_tasks"
            ? `ct:${d.convID}`
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
      className={`w-full h-8 px-2 rounded-[6px] flex items-center gap-2.5 text-[14px] transition-colors duration-100 ${
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
      className={`min-w-5 h-5 px-1.5 rounded-[5px] text-[11.5px] font-semibold tabular inline-flex items-center justify-center ${
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
    <button onClick={onClick} title={label} aria-label={label} className="size-6 rounded-[5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center">
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
  const toast = useToast();
  const bootQ = useQuery({ queryKey: ["bootstrap", spaceSlug], queryFn: () => api.bootstrap(spaceSlug) });
  const [theme, setTheme] = useTheme();
  const [mainView, setMainViewState] = useState<MainView>({ kind: "inbox" });
  const [detail, setDetail] = useState<DetailTarget | null>(null);
  const [inboxFilter, setInboxFilter] = useState<InboxFilter>("needs_me");
  const [taskFilter, setTaskFilter] = useState<TaskFilter>("active");
  const [dialog, setDialog] = useState<Dialog>(() => (readPendingPairing() ? "pairing" : null));
  const [copyAgent, setCopyAgent] = useState<Member | null>(null);
  const [editingGroup, setEditingGroup] = useState<ChannelGroup | null>(null);
  const [dragChannelID, setDragChannelID] = useState<string | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [reconnecting, setReconnecting] = useState(false);
  const [channelSort, setChannelSort] = useChannelSort();
  const [sidebarCollapsed, setSidebarCollapsed] = useSidebarCollapsed();
  const [recentConvIDs, setRecentConvIDs] = useState<string[]>(() => readRecentConvs(spaceSlug));
  const [ctxMenu, setCtxMenu] = useState<{ conv: Conversation; x: number; y: number } | null>(null);
  const dm = useAction();
  const groupAct = useAction();
  const landed = useRef(false);
  const historyRef = useRef<{ stack: MainView[]; index: number }>({ stack: [{ kind: "inbox" }], index: 0 });
  const boot = bootQ.data;
  const panes = usePaneWidths();
  const draftConvIDs = useDraftConvIDs();
  const accent = boot?.space.accent;
  useEffect(() => applyAccent(accent), [accent]);
  useEffect(() => setRecentConvIDs(readRecentConvs(spaceSlug)), [spaceSlug]);

  const setMainView = useCallback(
    (next: MainView, opts?: { replace?: boolean }) => {
      setMainViewState(next);
      if (next.kind === "conversation") {
        setRecentConvIDs(recordRecentConv(spaceSlug, next.convID));
      }
      const h = historyRef.current;
      if (opts?.replace) {
        h.stack[h.index] = next;
      } else {
        const cur = h.stack[h.index];
        const same =
          cur &&
          cur.kind === next.kind &&
          (cur.kind !== "conversation" || (next.kind === "conversation" && cur.convID === next.convID)) &&
          (cur.kind !== "admin" || (next.kind === "admin" && cur.tab === next.tab));
        if (!same) {
          h.stack = [...h.stack.slice(0, h.index + 1), next].slice(-30);
          h.index = h.stack.length - 1;
        }
      }
    },
    [spaceSlug],
  );

  const stepHistory = useCallback((delta: -1 | 1) => {
    const h = historyRef.current;
    const nextIdx = h.index + delta;
    if (nextIdx < 0 || nextIdx >= h.stack.length) return;
    h.index = nextIdx;
    setMainViewState(h.stack[nextIdx]);
  }, []);

  // Dismiss channel right-click context menu on outside click or Escape.
  useEffect(() => {
    if (!ctxMenu) return;
    const close = () => setCtxMenu(null);
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    };
    window.addEventListener("mousedown", close);
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("mousedown", close);
      window.removeEventListener("keydown", onKey);
    };
  }, [ctxMenu]);

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
    let reconnectTimer: number | null = null;

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
        case "agent.state_changed": {
          const memberID = typeof p.member_id === "string" ? p.member_id : "";
          const runtimeState = typeof p.runtime_state === "string" ? p.runtime_state : "";
          if (memberID && runtimeState) {
            let patched = false;
            qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], (prev) => {
              if (!prev) return prev;
              if (!prev.members.some((m) => m.id === memberID && m.agent)) return prev;
              patched = true;
              const focusID = typeof p.focus_conversation_id === "string" && p.focus_conversation_id ? p.focus_conversation_id : undefined;
              const errCode = typeof p.error_code === "string" && p.error_code ? p.error_code : undefined;
              const errDetail = typeof p.error_detail === "string" && p.error_detail ? p.error_detail : undefined;
              const activity = p.activity && typeof p.activity === "object" ? (p.activity as NonNullable<Member["agent"]>["activity"]) : undefined;
              return {
                ...prev,
                members: prev.members.map((m) =>
                  m.id === memberID && m.agent
                    ? {
                        ...m,
                        agent: {
                          ...m.agent,
                          runtime_state: runtimeState as NonNullable<Member["agent"]>["runtime_state"],
                          focus_conversation_id: focusID,
                          last_error_code: errCode,
                          last_error_detail: errDetail,
                          activity,
                        },
                      }
                    : m,
                ),
                conversations: prev.conversations.map((c) =>
                  c.kind === "dm" && c.peer_member_id === memberID
                    ? { ...c, peer_runtime_state: runtimeState as NonNullable<Member["agent"]>["runtime_state"] }
                    : c,
                ),
              };
            });
            if (patched) return;
          }
          // Fallback if the member isn't in bootstrap yet.
          if (stateTimer === null) {
            stateTimer = window.setTimeout(() => {
              stateTimer = null;
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
            }, 250);
          }
          return;
        }
        case "conversation.read_updated": {
          const memberID = typeof p.member_id === "string" ? p.member_id : "";
          const readSeq = typeof p.read_seq === "number" ? p.read_seq : 0;
          if (convID && memberID && readSeq > 0) {
            qc.setQueryData<{ messages: unknown[]; read_seq: number; following: boolean; has_more?: boolean; agent_reads?: { member_id: string; read_seq: number }[] }>(
              ["messages", spaceSlug, convID],
              (prev) => {
                if (!prev) return prev;
                const existing = prev.agent_reads ?? [];
                const found = existing.some((r) => r.member_id === memberID);
                const nextReads = found
                  ? existing.map((r) => (r.member_id === memberID ? { ...r, read_seq: Math.max(r.read_seq, readSeq) } : r))
                  : [...existing, { member_id: memberID, read_seq: readSeq }];
                return { ...prev, agent_reads: nextReads };
              },
            );
          }
          return;
        }
        case "message.reaction_changed":
          if (convID) void qc.invalidateQueries({ queryKey: ["messages", spaceSlug, convID] });
          else void qc.invalidateQueries({ queryKey: ["messages", spaceSlug] });
          return;
        case "message.created":
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
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      if (dropped) {
        dropped = false;
        setReconnecting(false);
        invalidateAll();
      }
    };
    es.onerror = () => {
      dropped = true;
      if (reconnectTimer === null) {
        reconnectTimer = window.setTimeout(() => {
          reconnectTimer = null;
          if (dropped) setReconnecting(true);
        }, 1800);
      }
    };
    for (const ev of SSE_EVENTS) es.addEventListener(ev, handleEvent(ev));
    return () => {
      if (stateTimer !== null) window.clearTimeout(stateTimer);
      if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
      setReconnecting(false);
      es.close();
    };
  }, [spaceSlug, qc]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setDialog((d) => (d === "jump" ? null : "jump"));
        return;
      }
      if (mod && (e.key.toLowerCase() === "b" || e.key === "\\")) {
        e.preventDefault();
        setSidebarCollapsed((v) => !v);
        return;
      }
      if (mod && e.key === "/") {
        e.preventDefault();
        setDialog((d) => (d === "shortcuts" ? null : "shortcuts"));
        return;
      }
      if ((e.metaKey && e.key === "[") || (!isMacPlatform() && e.altKey && e.key === "ArrowLeft")) {
        e.preventDefault();
        stepHistory(-1);
        return;
      }
      if ((e.metaKey && e.key === "]") || (!isMacPlatform() && e.altKey && e.key === "ArrowRight")) {
        e.preventDefault();
        stepHistory(1);
        return;
      }
      if (mod && !e.shiftKey && !e.altKey && /^[1-9]$/.test(e.key) && spaces.length > 1) {
        const targetSpace = spaces[Number(e.key) - 1];
        if (targetSpace && targetSpace.slug !== spaceSlug) {
          e.preventDefault();
          onSwitchSpace?.(targetSpace.slug);
          return;
        }
      }
      if (e.key === "Escape" && !dialog && detail) {
        const tag = (document.activeElement as HTMLElement | null)?.tagName;
        if (tag === "INPUT" || tag === "TEXTAREA" || document.querySelector('[role="dialog"],[role="listbox"],[role="menu"]')) return;
        setDetail(null);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [dialog, detail, setSidebarCollapsed, stepHistory, spaces, spaceSlug, onSwitchSpace]);

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

  if (!boot) {
    if (bootQ.isError) {
      return (
        <div className="h-full bg-bg flex items-center justify-center">
          <ErrorState
            title="Couldn't load space"
            message={bootQ.error instanceof Error ? bootQ.error.message : "Check your connection and try again."}
            onRetry={() => void bootQ.refetch()}
          />
        </div>
      );
    }
    return <WorkbenchSkeleton />;
  }

  const channels = boot.conversations.filter((c) => c.kind === "public" || c.kind === "private");
  const channelGroups = boot.channel_groups ?? [];
  const channelByID = new Map(channels.map((c) => [c.id, c]));
  const groupedConvIDs = new Set(channelGroups.flatMap((g) => g.conversation_ids));
  const ungroupedChannels = sortChannelsList(
    channels.filter((c) => !groupedConvIDs.has(c.id)),
    channelSort,
  );
  const dms = boot.conversations.filter((c) => c.kind === "dm");
  const agents = boot.members.filter((m) => m.kind === "agent");
  const people = boot.members.filter((m) => m.kind === "human" && m.id !== boot.current_member.id);
  const onlineComputers = boot.computers.filter((c) => c.connection_status === "online").length;
  const canGovern = boot.current_member.access_level !== "member";
  const myComputers = ownComputers(boot);
  const openNewAgentDialog = (source?: Member) => {
    setCopyAgent(source ?? null);
    setDialog(myComputers.length ? "agent" : "computer");
  };
  const needsSetup = canGovern && (myComputers.length === 0 || agents.length === 0);
  const needsMe = boot.inbox.filter((i) => i.needs_attention).length;
  const pairing = readPendingPairing();
  const desktop = isDesktopShell();
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

  function openChannelContextMenu(e: ReactMouseEvent, c: Conversation) {
    e.preventDefault();
    const x = Math.min(e.clientX, (window.innerWidth || 1024) - 224);
    const y = Math.min(e.clientY, (window.innerHeight || 768) - 260);
    setCtxMenu({ conv: c, x: Math.max(8, x), y: Math.max(8, y) });
  }

  function setChannelNotify(c: Conversation, level: NotifyLevel) {
    const prev = qc.getQueryData<BootstrapData>(["bootstrap", spaceSlug]);
    if (prev) {
      qc.setQueryData<BootstrapData>(["bootstrap", spaceSlug], {
        ...prev,
        conversations: prev.conversations.map((item) => (item.id === c.id ? { ...item, notify_level: level } : item)),
      });
    }
    void groupAct.run(async () => {
      try {
        await api.setNotifyLevel(spaceSlug, c.id, level);
      } catch (err) {
        if (prev) qc.setQueryData(["bootstrap", spaceSlug], prev);
        throw err;
      }
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
    });
  }

  const renderChannelRow = (c: Conversation) => {
    const muted = c.notify_level === "mute";
    const hasDraft = draftConvIDs.has(c.id) && activeConvID !== c.id;
    return (
      <div
        key={c.id}
        draggable
        onDragStart={(e) => handleChannelDragStart(e, c.id)}
        onDragEnd={handleChannelDragEnd}
        onContextMenu={(e) => openChannelContextMenu(e, c)}
        className={dragChannelID === c.id ? "opacity-45" : ""}
      >
        <NavItem
          active={activeConvID === c.id}
          onClick={() => setMainView({ kind: "conversation", convID: c.id })}
          icon={c.kind === "private" ? <Lock /> : <Hash />}
          dim={muted}
          bold={c.unread_count > 0 && !muted}
          trailing={
            <span className="flex items-center gap-1.5 shrink-0">
              {hasDraft && <Pencil className="size-3 text-nav-fg-3" aria-label="Has draft" />}
              {c.mention_count > 0 ? <Count n={c.mention_count} /> : c.unread_count > 0 && !muted ? <span className="size-2 rounded-full bg-nav-fg-strong shrink-0 mr-1" /> : null}
            </span>
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
    return (
      <button
        key={m.id}
        onClick={() => void openOrCreateDM(m.id)}
        aria-current={active ? "page" : undefined}
        className={`w-full px-2 ${m.kind === "agent" ? "h-11" : "h-9"} rounded-[6px] flex items-center gap-2.5 text-left transition-colors duration-100 ${
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
        className={`relative size-9 rounded-[7px] flex items-center justify-center transition-colors [&>svg]:size-[18px] ${
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

  const navWidth = sidebarCollapsed ? 56 : 56 + panes.sidebar;

  return (
    <div
      className="h-full grid bg-bg"
      style={{ gridTemplateColumns: `${navWidth}px minmax(0, 1fr) ${detail ? panes.detail : 0}px` }}
    >
      {/* Navigation shell: in desktop mode a unified 52px header spans rail + sidebar so macOS traffic lights sit on one continuous surface. */}
      <div className="relative flex flex-col min-w-0 min-h-0 border-r border-nav-line bg-nav select-none">
        {!sidebarCollapsed && (
          <ResizeHandle
            label="Resize sidebar"
            edge="right"
            value={panes.sidebar}
            min={SIDEBAR_MIN}
            max={SIDEBAR_MAX}
            onResize={panes.setSidebar}
            onReset={panes.resetSidebar}
          />
        )}
        {desktop && (
          <div
            data-desktop-titlebar
            className={`h-[52px] ${sidebarCollapsed ? "pl-3 justify-center" : "pl-[92px] pr-3 justify-between"} border-b border-nav-line flex items-center gap-2 shrink-0 titlebar-drag`}
          >
            {!sidebarCollapsed && <span className="text-[15px] font-semibold text-nav-fg-strong truncate">{boot.space.name}</span>}
            {!sidebarCollapsed && (
              <WorkingNowSummary
                boot={boot}
                onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
                onOpenDetail={setDetail}
              />
            )}
          </div>
        )}
        <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: sidebarCollapsed ? "56px" : "56px minmax(0, 1fr)" }}>
          {/* Rail */}
          <nav aria-label="Spaces" className="bg-nav-rail flex flex-col items-center justify-between pb-3">
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
                      className={`size-9 rounded-[8px] text-[14px] font-semibold flex items-center justify-center transition-[background-color,border-radius,opacity] ${
                        active ? "text-white shadow-sm" : "opacity-55 hover:opacity-90 hover:rounded-[6px] text-white"
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
                className="size-9 rounded-[8px] border border-dashed border-nav-line-strong text-nav-fg-3 hover:text-nav-fg-strong hover:border-nav-fg-3 flex items-center justify-center transition-colors"
              >
                <Plus className="size-4" />
              </button>
            </div>
            <div className="flex flex-col items-center gap-1">
              <button
                type="button"
                onClick={() => setSidebarCollapsed((v) => !v)}
                title={sidebarCollapsed ? `Expand sidebar (${isMacPlatform() ? "⌘B" : "Ctrl+B"})` : `Collapse sidebar (${isMacPlatform() ? "⌘B" : "Ctrl+B"})`}
                aria-label={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"}
                className="size-9 rounded-[7px] flex items-center justify-center text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong transition-colors"
              >
                <PanelLeft className="size-[18px]" />
              </button>
              {railBtn("agents", <Bot />, "Agents")}
              {railBtn("computers", <Monitor />, `Computers · ${onlineComputers} online`, onlineComputers > 0)}
              {railBtn("people", <Users />, "People")}
              {railBtn("settings", <Settings />, "Settings")}
              <DesktopServerTrigger variant="rail" />
            </div>
          </nav>

          {/* Sidebar */}
          {!sidebarCollapsed && (
          <aside aria-label="Sidebar" className="bg-nav flex flex-col min-w-0 min-h-0">
            {!desktop && (
              <div className="h-[52px] px-4 flex items-center justify-between gap-2 shrink-0">
                <span className="text-[15px] font-semibold text-nav-fg-strong truncate">{boot.space.name}</span>
                <WorkingNowSummary
                  boot={boot}
                  onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
                  onOpenDetail={setDetail}
                />
              </div>
            )}
            <div className={`px-3 pb-2 shrink-0 ${desktop ? "pt-3" : ""}`}>
          <button
            onClick={() => setDialog("jump")}
            className="w-full h-9 px-3 rounded-[6px] bg-nav-fill hover:bg-nav-fill-2 border border-nav-line text-nav-fg-3 flex items-center gap-2 text-[13.5px] transition-colors"
          >
            <Search className="size-4" />
            <span className="flex-1 text-left">Jump to…</span>
            <kbd className="text-[11.5px] font-sans px-1.5 h-5 rounded-[4px] bg-nav-fill-2 inline-flex items-center">⌘K</kbd>
          </button>
        </div>

        <div className="relative flex-1 min-h-0 flex flex-col">
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
            className={`rounded-[7px] transition-colors ${dropTarget === "__ungrouped__" ? "ring-1 ring-accent bg-accent-soft/30" : ""}`}
          >
            <SectionLabel
              action={
                <div className="flex items-center gap-0.5">
                  <Menu
                    width={176}
                    trigger={(open, toggle) => (
                      <button
                        type="button"
                        onClick={toggle}
                        aria-expanded={open}
                        title={`Sort channels (${channelSort})`}
                        aria-label="Sort channels"
                        className={`size-6 rounded-[5px] flex items-center justify-center transition-colors ${
                          open || channelSort !== "default" ? "text-nav-fg-strong bg-nav-hover" : "text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong"
                        }`}
                      >
                        <ArrowDownUp className="size-3.5" />
                      </button>
                    )}
                  >
                    {(close) => (
                      <>
                        {(
                          [
                            ["default", "Default order"],
                            ["unread", "Unread first"],
                            ["name", "A–Z"],
                          ] as const
                        ).map(([mode, label]) => (
                          <MenuItem
                            key={mode}
                            active={channelSort === mode}
                            onSelect={() => {
                              setChannelSort(mode);
                              close();
                            }}
                          >
                            {label}
                          </MenuItem>
                        ))}
                      </>
                    )}
                  </Menu>
                  <button
                    onClick={() => {
                      setEditingGroup(null);
                      setDialog("group");
                    }}
                    title="New channel group"
                    aria-label="New channel group"
                    className="size-6 rounded-[5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center"
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
                <div className="px-2 py-2 text-[12px] text-nav-fg-3 text-center rounded-[6px] border border-dashed border-nav-line-strong">
                  Drop here to ungroup
                </div>
              )}
            </div>
          </div>

          {channelGroups.map((g) => {
            const items = sortChannelsList(
              g.conversation_ids.map((id) => channelByID.get(id)).filter((c): c is Conversation => Boolean(c)),
              channelSort,
            );
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
                className={`rounded-[7px] transition-colors ${isOver ? "ring-1 ring-accent bg-accent-soft/30" : ""}`}
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
            <SectionLabel action={canGovern ? <SectionAdd label="New agent" onClick={() => openNewAgentDialog()} /> : undefined}>
              Agents
            </SectionLabel>
            <div className="space-y-0.5">
              {agents.map(memberRow)}
              {agents.length === 0 && (
                <button
                  onClick={() => openNewAgentDialog()}
                  className="w-full h-9 px-2 rounded-[6px] flex items-center gap-2.5 text-[13.5px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg"
                >
                  <span className="size-6 rounded-[5px] border border-dashed border-nav-line-strong flex items-center justify-center">
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

        {reconnecting && (
          <div
            role="status"
            className="pointer-events-none absolute bottom-2 inset-x-3 px-2.5 h-8 rounded-[6px] bg-busy-soft border border-busy/25 text-nav-busy shadow-sm flex items-center gap-2 text-[12.5px] font-medium animate-fade-in"
          >
            <span className="size-2 rounded-full bg-busy animate-breathe shrink-0" />
            <span>Reconnecting…</span>
          </div>
        )}
        </div>

        {needsSetup && (
          <div className="px-3 pb-3 shrink-0">
            <button
              onClick={() => openNewAgentDialog()}
              className="w-full p-3 rounded-[8px] bg-gradient-to-br from-accent/30 to-accent/10 border border-accent/30 hover:border-accent/60 text-left transition-colors"
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

        <div className="h-14 px-3 border-t border-nav-line flex items-center gap-2.5 shrink-0">
          <button onClick={() => setMainView({ kind: "admin", tab: "settings" })} className="flex items-center gap-2.5 min-w-0 flex-1 rounded-[6px] p-1 -m-1 hover:bg-nav-hover">
            <MemberAvatar id={boot.current_member.id} name={boot.current_member.display_name} kind="human" size="sm" />
            <span className="min-w-0 flex-1 text-left text-[14px] leading-5 font-medium text-nav-fg-strong truncate">{boot.current_member.display_name}</span>
          </button>
          <button
            onClick={() => setTheme(dark ? "light" : "dark")}
            aria-label={dark ? "Switch to light mode" : "Switch to dark mode"}
            title={dark ? "Light mode" : "Dark mode"}
            className="size-8 rounded-[6px] text-nav-fg-3 hover:bg-nav-hover hover:text-nav-fg-strong flex items-center justify-center"
          >
            {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
          </button>
        </div>
          </aside>
          )}
        </div>
      </div>

      {/* Main */}
      <main className="bg-panel flex flex-col min-w-0 min-h-0">
        <div className="h-full min-h-0">
          <ErrorBoundary
            resetKey={mainView.kind === "conversation" ? `c:${mainView.convID}` : mainView.kind === "admin" ? `a:${mainView.tab}` : mainView.kind}
            label="main view"
          >
            {mainView.kind === "conversation" && activeConv && (
              <ConversationView
                key={activeConv.id}
                spaceSlug={spaceSlug}
                boot={boot}
                conv={activeConv}
                focusMessageID={mainView.focusMessageID}
                onOpenDetail={setDetail}
                onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
                onOpenDM={(memberID) => void openOrCreateDM(memberID)}
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
                onNewAgent={() => openNewAgentDialog()}
                onCopyAgent={canGovern ? (agent) => openNewAgentDialog(agent) : undefined}
                onConnectComputer={() => {
                  setCopyAgent(null);
                  setDialog("computer");
                }}
                onOpenArchivedChannel={(conv) => setMainView({ kind: "conversation", convID: conv.id, archivedConv: conv })}
                theme={theme}
                onTheme={setTheme}
                onLogout={onLogout}
              />
            )}
          </ErrorBoundary>
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
          <div className="h-full overflow-hidden animate-slide-in" style={{ width: panes.detail }}>
            <ErrorBoundary resetKey={detailKey(detail)} label="detail panel">
              <DetailPane
                key={detailKey(detail)}
                spaceSlug={spaceSlug}
                boot={boot}
                target={detail}
                onClose={() => setDetail(null)}
                onSelectTarget={setDetail}
                onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
                onOpenDM={(memberID) => void openOrCreateDM(memberID)}
                onCopyAgent={canGovern ? (agent) => openNewAgentDialog(agent) : undefined}
              />
            </ErrorBoundary>
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
      <Modal
        open={dialog === "computer"}
        onClose={() => {
          setDialog(null);
          setCopyAgent(null);
        }}
        label="Connect a computer"
        width={520}
      >
        <div className="p-6">
          <ComputerStep
            spaceSlug={spaceSlug}
            nextLabel={copyAgent || agents.length === 0 ? "Next: create an agent" : "Done"}
            onNext={() => setDialog(copyAgent || agents.length === 0 ? "agent" : null)}
          />
        </div>
      </Modal>
      <Modal
        open={dialog === "agent"}
        onClose={() => {
          setDialog(null);
          setCopyAgent(null);
        }}
        label="New agent"
        title="New agent"
        description={
          copyAgent
            ? `Starting from ${copyAgent.display_name}'s configuration.`
            : "A persistent teammate with its own role, memory and workspace."
        }
        width={540}
      >
        <div className="px-6 pb-6 pt-4">
          <AgentForm
            key={copyAgent?.id ?? "new"}
            spaceSlug={spaceSlug}
            computers={myComputers}
            initialAgent={copyAgent ?? undefined}
            existingMembers={boot.members}
            onCreated={(agent) => {
              setDialog(null);
              setCopyAgent(null);
              void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] }).then(() => openOrCreateDM(agent.id));
            }}
            footer={(submit) => (
              <div className="flex justify-end gap-2 pt-1">
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => {
                    setDialog(null);
                    setCopyAgent(null);
                  }}
                >
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
        spaceSlug={spaceSlug}
        onSelectMessageHit={(hit: SearchHit) => {
          if (hit.conversation_kind === "task" && hit.task_id) {
            setDetail({ kind: "task", taskID: hit.task_id });
          } else if (hit.conversation_kind === "thread") {
            if (hit.parent_conversation_id) {
              setMainView({ kind: "conversation", convID: hit.parent_conversation_id });
            }
            setDetail({
              kind: "thread",
              threadConvID: hit.conversation_id,
              rootMessageID: hit.root_message_id ?? "",
              parentSlug: hit.parent_slug ?? "",
            });
          } else {
            setMainView({ kind: "conversation", convID: hit.conversation_id, focusMessageID: hit.message_id });
          }
        }}
        items={[
          ...recentConvIDs
            .map((id) => boot.conversations.find((c) => c.id === id))
            .filter((c): c is Conversation => Boolean(c && (c.kind === "public" || c.kind === "private" || c.kind === "dm")))
            .slice(0, 4)
            .map(
              (c): JumpItem => ({
                key: `recent:${c.id}`,
                label: c.kind === "dm" ? c.peer_display_name ?? "Direct message" : `#${c.slug ?? ""}`,
                keywords: c.topic ?? "",
                group: "Recent",
                icon: <Clock className="size-4" />,
                run: () => setMainView({ kind: "conversation", convID: c.id }),
              }),
            ),
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
            key: "act:sidebar",
            label: sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar",
            keywords: "sidebar navigation toggle",
            group: "Actions",
            icon: <PanelLeft className="size-4" />,
            shortcut: isMacPlatform() ? "⌘B" : "Ctrl+B",
            run: () => setSidebarCollapsed((v) => !v),
          },
          {
            key: "act:shortcuts",
            label: "Keyboard shortcuts",
            keywords: "keys hotkeys help",
            group: "Actions",
            icon: <Keyboard className="size-4" />,
            shortcut: isMacPlatform() ? "⌘/" : "Ctrl+/",
            run: () => setDialog("shortcuts"),
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
      <ShortcutsDialog open={dialog === "shortcuts"} onClose={() => setDialog(null)} />
      {ctxMenu &&
        createPortal(
          <div
            role="menu"
            aria-label={`#${ctxMenu.conv.slug} menu`}
            style={{ position: "fixed", left: ctxMenu.x, top: ctxMenu.y, width: 216 }}
            onMouseDown={(e) => e.stopPropagation()}
            className="z-50 bg-card border border-line rounded-[8px] shadow-lg p-1 animate-pop-in"
          >
            {ctxMenu.conv.unread_count > 0 && (
              <MenuItem
                icon={<Check />}
                onSelect={() => {
                  const target = ctxMenu.conv;
                  setCtxMenu(null);
                  void groupAct.run(async () => {
                    await api.markDone(spaceSlug, target.id);
                    void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                    void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
                  });
                }}
              >
                Mark as read
              </MenuItem>
            )}
            <div className="px-2.5 pt-1.5 pb-0.5 text-[11px] font-semibold uppercase tracking-[0.04em] text-fg-4">Notifications</div>
            {(
              [
                ["mentions", "Mentions only", <AtSign key="m" />],
                ["all", "All messages", <Bell key="a" />],
                ["mute", "Muted", <BellOff key="u" />],
              ] as const
            ).map(([level, label, icon]) => (
              <MenuItem
                key={level}
                active={(ctxMenu.conv.notify_level ?? "mentions") === level}
                icon={icon}
                onSelect={() => {
                  setChannelNotify(ctxMenu.conv, level);
                  setCtxMenu(null);
                }}
              >
                {label}
              </MenuItem>
            ))}
            {channelGroups.length > 0 && (
              <>
                <div className="my-1 border-t border-line" />
                <div className="px-2.5 pt-1 pb-0.5 text-[11px] font-semibold uppercase tracking-[0.04em] text-fg-4">Move to group</div>
                {channelGroups.map((g) => {
                  const inGroup = g.conversation_ids.includes(ctxMenu.conv.id);
                  return (
                    <MenuItem
                      key={g.id}
                      active={inGroup}
                      icon={<FolderInput />}
                      onSelect={() => {
                        const target = ctxMenu.conv;
                        setCtxMenu(null);
                        void groupAct.run(async () => {
                          await api.setConversationGroup(spaceSlug, target.id, inGroup ? "" : g.id);
                          void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                        });
                      }}
                    >
                      {g.name}
                    </MenuItem>
                  );
                })}
                {channelGroups.some((g) => g.conversation_ids.includes(ctxMenu.conv.id)) && (
                  <MenuItem
                    icon={<X />}
                    onSelect={() => {
                      const target = ctxMenu.conv;
                      setCtxMenu(null);
                      void groupAct.run(async () => {
                        await api.setConversationGroup(spaceSlug, target.id, "");
                        void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
                      });
                    }}
                  >
                    Remove from group
                  </MenuItem>
                )}
              </>
            )}
            <div className="my-1 border-t border-line" />
            <MenuItem
              icon={<Copy />}
              onSelect={() => {
                const slug = ctxMenu.conv.slug ?? "";
                setCtxMenu(null);
                void navigator.clipboard?.writeText(`#${slug}`).then(() => toast(`Copied #${slug}`, "success"));
              }}
            >
              Copy #{ctxMenu.conv.slug}
            </MenuItem>
          </div>,
          document.body,
        )}
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
