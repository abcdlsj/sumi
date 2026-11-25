import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { CheckCheck, MessageSquare, PanelRight, Square } from "lucide-react";
import {
  activityLabel,
  formatClock,
  type AgentReadProgress,
  type BootstrapData,
  type Member,
  type Message,
} from "../api";
import { agentFocus, agentStatusText, MemberAvatar, stateLabel, StatusDot } from "./Avatar";
import type { DetailTarget } from "./DetailPane";
import { Badge, useFloatingPosition } from "./ui";

const MAX_READ_NAMES = 3;

/**
 * Shared popover listing working agents with their live focus conversation/task and tool activity.
 */
export function WorkingAgentsPopover({
  boot,
  agents,
  open,
  triggerRef,
  align = "right",
  onClose,
  onOpenConversation,
  onOpenDetail,
  onStopAgent,
}: {
  boot: BootstrapData;
  agents: Member[];
  open: boolean;
  triggerRef: React.RefObject<HTMLElement | null>;
  align?: "left" | "right";
  onClose: () => void;
  onOpenConversation?: (convID: string) => void;
  onOpenDetail: (t: DetailTarget) => void;
  onStopAgent?: (agentID: string) => void;
}) {
  const popRef = useRef<HTMLDivElement | null>(null);
  const style = useFloatingPosition(open, triggerRef, popRef, { align, width: 300, maxHeight: 340 });

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (triggerRef.current?.contains(target) || popRef.current?.contains(target)) return;
      onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopImmediatePropagation();
        onClose();
      }
    };
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open, onClose, triggerRef]);

  if (!open || agents.length === 0) return null;

  return createPortal(
    <div
      ref={popRef}
      role="dialog"
      aria-label="Working now"
      style={style}
      className="z-50 bg-card border border-line-strong rounded-[6px] shadow-lg overflow-hidden animate-pop-in"
    >
      <div className="h-9 px-3 border-b border-line flex items-center justify-between text-[12px] font-semibold text-fg-2">
        <span>Working now</span>
        <span className="tabular font-mono text-fg-4">{agents.length}</span>
      </div>
      <div className="max-h-[296px] overflow-y-auto scroll-thin p-1 space-y-0.5">
        {agents.map((a) => {
          const focus = agentFocus(boot, a);
          const act = activityLabel(a.agent?.activity);
          return (
            <div key={a.id} className="rounded-[6px] hover:bg-subtle p-2 flex items-start gap-2.5 transition-colors">
              <button
                type="button"
                onClick={() => {
                  onClose();
                  onOpenDetail({ kind: "agent", memberID: a.id });
                }}
                className="shrink-0 mt-0.5"
                aria-label={`Inspect ${a.display_name}`}
              >
                <MemberAvatar id={a.id} name={a.display_name} kind="agent" size="sm" state={a.agent?.runtime_state} ring="var(--card)" />
              </button>
              <div className="min-w-0 flex-1">
                <div className="flex items-center justify-between gap-2">
                  <button
                    type="button"
                    onClick={() => {
                      onClose();
                      if (focus?.taskID) onOpenDetail({ kind: "task", taskID: focus.taskID });
                      else if (focus?.convID && onOpenConversation) onOpenConversation(focus.convID);
                      else onOpenDetail({ kind: "agent", memberID: a.id });
                    }}
                    className="text-[13.5px] font-semibold text-fg hover:underline underline-offset-2 truncate text-left"
                  >
                    {a.display_name}
                  </button>
                  <div className="flex items-center gap-0.5 shrink-0">
                    {onStopAgent && (
                      <button
                        type="button"
                        onClick={() => onStopAgent(a.id)}
                        title={`Stop ${a.display_name}`}
                        aria-label={`Stop ${a.display_name}`}
                        className="size-6 rounded-[4px] text-fg-4 hover:text-danger hover:bg-hover flex items-center justify-center shrink-0"
                      >
                        <Square className="size-2.5 fill-current" />
                      </button>
                    )}
                    <button
                      type="button"
                      onClick={() => {
                        onClose();
                        onOpenDetail({ kind: "agent", memberID: a.id });
                      }}
                      title={`Inspect ${a.display_name}`}
                      aria-label={`Inspect ${a.display_name}`}
                      className="size-6 rounded-[4px] text-fg-4 hover:text-fg hover:bg-hover flex items-center justify-center shrink-0"
                    >
                      <PanelRight className="size-3.5" />
                    </button>
                  </div>
                </div>
                {focus && (
                  <div className="text-[12px] text-fg-3 truncate">in {focus.label}</div>
                )}
                {act && (
                  <div className={`mt-0.5 font-mono text-[11.5px] truncate ${a.agent?.activity?.stalled_seconds ? "text-warm" : "text-busy"}`}>
                    {act}
                  </div>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>,
    document.body,
  );
}

/** Maps each rendered text message id to the agents whose read watermark stops on that message (excluding the author). */
export function buildAgentReadMap(
  messages: Message[] | undefined,
  agentReads: AgentReadProgress[] | undefined,
  members: Member[],
): Map<string, Member[]> {
  const map = new Map<string, Member[]>();
  if (!messages || messages.length === 0 || !agentReads || agentReads.length === 0) return map;
  const byID = new Map(members.map((m) => [m.id, m]));
  for (const r of agentReads) {
    const member = byID.get(r.member_id);
    if (!member || member.kind !== "agent") continue;
    let target: Message | undefined;
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].kind === "text" && messages[i].seq <= r.read_seq) {
        target = messages[i];
        break;
      }
    }
    if (!target || target.author_member_id === member.id) continue;
    const list = map.get(target.id);
    if (list) list.push(member);
    else map.set(target.id, [member]);
  }
  return map;
}

/** Quiet trailing read line under the newest message each agent has read. */
export function AgentReadReceipts({
  agents,
  boot,
  onSelectAgent,
}: {
  agents?: Member[];
  boot: BootstrapData;
  onSelectAgent?: (memberID: string) => void;
}) {
  if (!agents || agents.length === 0) return null;
  const shown = agents.slice(0, MAX_READ_NAMES);
  const overflow = agents.length - shown.length;
  const label = agents
    .map((a) => {
      const st = a.agent?.runtime_state;
      return st === "working" ? `${a.display_name} (${agentStatusText(boot, a)})` : a.display_name;
    })
    .join(", ");

  return (
    <div
      data-testid="agent-read-receipts"
      title={`Read by ${label}`}
      aria-label={`Read by ${label}`}
      className="mt-0.5 flex items-center justify-end gap-1 text-[11px] leading-4 text-fg-4 select-none"
    >
      <CheckCheck className="size-3 shrink-0 opacity-75" aria-hidden />
      <span className="inline-flex items-center flex-wrap justify-end gap-x-1">
        {shown.map((a, idx) => (
          <span key={a.id} className="inline-flex items-center">
            <button
              type="button"
              onClick={() => onSelectAgent?.(a.id)}
              title={
                a.agent?.runtime_state === "working"
                  ? `Read up to here · ${a.display_name} (${agentStatusText(boot, a)})`
                  : `Read up to here · ${a.display_name}`
              }
              className="hover:text-fg-2 transition-colors"
            >
              {a.display_name}
            </button>
            {idx < shown.length - 1 && <span>,</span>}
          </span>
        ))}
        {overflow > 0 && <span className="tabular">+{overflow}</span>}
      </span>
    </div>
  );
}

/** Lightweight hover preview card for an agent or human without replacing the open DetailPane. */
export function MemberHoverCard({
  boot,
  member,
  onInspect,
  onMessage,
  children,
}: {
  boot: BootstrapData;
  member?: Member;
  onInspect?: (member: Member) => void;
  onMessage?: (memberID: string) => void;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLSpanElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);
  const openTimer = useRef<number | null>(null);
  const closeTimer = useRef<number | null>(null);
  const style = useFloatingPosition(open, triggerRef, cardRef, { align: "left", width: 272, maxHeight: 220 });

  useEffect(() => {
    return () => {
      if (openTimer.current) window.clearTimeout(openTimer.current);
      if (closeTimer.current) window.clearTimeout(closeTimer.current);
    };
  }, []);

  if (!member) return <>{children}</>;

  const scheduleOpen = () => {
    if (closeTimer.current) {
      window.clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
    if (open) return;
    openTimer.current = window.setTimeout(() => setOpen(true), 320);
  };

  const scheduleClose = () => {
    if (openTimer.current) {
      window.clearTimeout(openTimer.current);
      openTimer.current = null;
    }
    closeTimer.current = window.setTimeout(() => setOpen(false), 160);
  };

  const ag = member.agent;
  const st = ag?.runtime_state;
  const focus = ag ? agentFocus(boot, member) : null;
  const act = ag ? activityLabel(ag.activity) : null;
  const isSelf = member.id === boot.current_member.id;

  return (
    <>
      <span
        ref={triggerRef}
        onMouseEnter={scheduleOpen}
        onMouseLeave={scheduleClose}
        className="inline-flex items-center"
      >
        {children}
      </span>
      {open &&
        createPortal(
          <div
            ref={cardRef}
            role="tooltip"
            onMouseEnter={scheduleOpen}
            onMouseLeave={scheduleClose}
            style={style}
            className="z-50 p-3 rounded-[6px] bg-card border border-line-strong shadow-lg animate-pop-in"
          >
            <div className="flex items-start gap-2.5">
              <MemberAvatar id={member.id} name={member.display_name} kind={member.kind} size="md" state={st} ring="var(--card)" />
              <div className="min-w-0 flex-1">
                <div className="text-[14px] font-semibold text-fg truncate">{member.display_name}</div>
                <div className="mt-0.5 flex items-center gap-1.5 flex-wrap">
                  {member.kind === "agent" ? (
                    <Badge tone={st === "working" ? "busy" : st === "error" ? "danger" : "neutral"}>
                      <StatusDot state={st} size={6} ring="transparent" />
                      {stateLabel(st)}
                    </Badge>
                  ) : (
                    <Badge className="capitalize">{member.access_level}</Badge>
                  )}
                </div>
              </div>
            </div>
            {ag?.role_text && (
              <p className="mt-2 text-[12.5px] text-fg-2 line-clamp-2 leading-snug font-body">{ag.role_text}</p>
            )}
            {st === "working" && (focus || act) && (
              <div className="mt-2 px-2 py-1.5 rounded-[5px] bg-busy-soft/60 text-[12px]">
                {focus && <div className="text-fg-2 truncate">Working in <span className="font-medium text-fg">{focus.label}</span></div>}
                {act && <div className="font-mono text-[11px] text-busy truncate">{act}</div>}
              </div>
            )}
            {(onInspect || (onMessage && !isSelf)) && (
              <div className="mt-2.5 pt-2 border-t border-line flex items-center gap-1.5">
                {onInspect && (
                  <button
                    type="button"
                    onClick={() => {
                      setOpen(false);
                      onInspect(member);
                    }}
                    className="h-7 px-2.5 rounded-[5px] bg-subtle hover:bg-hover text-[12px] font-medium text-fg-2 hover:text-fg inline-flex items-center gap-1.5 transition-colors"
                  >
                    <PanelRight className="size-3.5" />
                    Inspect
                  </button>
                )}
                {onMessage && !isSelf && (
                  <button
                    type="button"
                    onClick={() => {
                      setOpen(false);
                      onMessage(member.id);
                    }}
                    className="h-7 px-2.5 rounded-[5px] bg-subtle hover:bg-hover text-[12px] font-medium text-fg-2 hover:text-fg inline-flex items-center gap-1.5 transition-colors"
                  >
                    <MessageSquare className="size-3.5" />
                    Message
                  </button>
                )}
              </div>
            )}
          </div>,
          document.body,
        )}
    </>
  );
}

/**
 * Slim right-edge tick rail inside ThreadDetail marking human messages (plus root)
 * so long agent replies never bury what humans asked.
 */
export function ThreadOutlineRail({
  messages,
  selfID,
  onJump,
}: {
  messages: Message[];
  selfID: string;
  onJump: (messageID: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const closeTimer = useRef<number | null>(null);
  const items = useMemo(
    () =>
      messages.filter((m, idx) => m.kind === "text" && (idx === 0 || m.author_kind === "human")),
    [messages],
  );

  useEffect(() => {
    return () => {
      if (closeTimer.current) window.clearTimeout(closeTimer.current);
    };
  }, []);

  if (items.length < 2) return null;

  const enter = () => {
    if (closeTimer.current) {
      window.clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
    setOpen(true);
  };
  const leave = () => {
    closeTimer.current = window.setTimeout(() => setOpen(false), 140);
  };

  return (
    <div
      data-testid="thread-outline-rail"
      onMouseEnter={enter}
      onMouseLeave={leave}
      className="absolute right-1.5 top-1/2 -translate-y-1/2 z-20 flex items-center"
    >
      {open && (
        <div
          role="navigation"
          aria-label="Thread outline"
          className="mr-1.5 w-[240px] max-h-[280px] overflow-y-auto scroll-thin rounded-[6px] bg-card border border-line-strong shadow-lg p-1.5 animate-pop-in"
        >
          <div className="px-2 py-1 text-[11px] font-semibold uppercase tracking-[0.04em] text-fg-4">
            Thread outline ({items.length})
          </div>
          {items.map((m) => {
            const preview = m.body_markdown.replace(/\s+/g, " ").trim().slice(0, 80) || "(attachment)";
            return (
              <button
                key={m.id}
                type="button"
                onClick={() => {
                  setOpen(false);
                  onJump(m.id);
                }}
                className="w-full px-2 py-1.5 rounded-[5px] hover:bg-hover text-left transition-colors block"
              >
                <div className="flex items-center justify-between gap-2 text-[11.5px]">
                  <span className="font-semibold text-fg truncate">{m.author_display_name}</span>
                  <span className="tabular text-fg-4 shrink-0">{formatClock(m.created_at)}</span>
                </div>
                <div className="text-[12px] text-fg-3 truncate">{preview}</div>
              </button>
            );
          })}
        </div>
      )}
      <button
        type="button"
        aria-label="Thread outline"
        onClick={() => setOpen((v) => !v)}
        className="py-2 px-1 flex flex-col items-end gap-1.5 rounded-[5px] hover:bg-subtle/80 transition-colors"
      >
        {items.slice(0, 18).map((m) => {
          const own = m.author_member_id === selfID;
          return (
            <span
              key={m.id}
              className={`block h-[2.5px] rounded-full transition-all ${
                own ? "w-3.5 bg-fg-2" : "w-2.5 bg-line-strong"
              }`}
            />
          );
        })}
      </button>
    </div>
  );
}
