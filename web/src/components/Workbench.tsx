import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, CheckCircle2, Hash, Inbox, Lock, Monitor, Moon, Plus, Search, Settings, Sparkles, Sun, Users } from "lucide-react";
import { api, ownComputers, slugInput, slugify, type Member, type Space } from "../api";
import { accentSwatch, applyAccent } from "../accent";
import { readPendingPairing } from "../pairing";
import { useTheme } from "../theme";
import { AgentForm } from "./AgentForm";
import { agentStatusText, MemberAvatar } from "./Avatar";
import { ConversationView } from "./ConversationView";
import { DetailPane, type DetailTarget } from "./DetailPane";
import { ComputerStep, PairingCard } from "./Setup";
import { DETAIL_MIN, ResizeHandle, SIDEBAR_MAX, SIDEBAR_MIN, usePaneWidths } from "./Resize";
import { WorkbenchSkeleton } from "./Skeletons";
import { Button, Field, Modal, Segmented, useAction } from "./ui";
import { AdminPanel, InboxView, TasksView, type AdminTab, type InboxFilter, type TaskFilter } from "./Views";

type MainView = { kind: "conversation"; convID: string } | { kind: "inbox" } | { kind: "tasks" } | { kind: "admin"; tab: AdminTab };
type Dialog = null | "channel" | "space" | "jump" | "computer" | "agent" | "pairing";

const SSE_EVENTS = [
  "message.created",
  "task.created",
  "task.updated",
  "agent.created",
  "agent.updated",
  "agent.state_changed",
  "computer.changed",
  "conversation.created",
  "member.permissions_changed",
  "conversation.members_changed",
  "space.updated",
];

/** Remount (and replay the slide-in) only when the panel shows a different object. */
function detailKey(d: DetailTarget) {
  return d.kind === "task" ? `t:${d.taskID}` : d.kind === "thread" ? `th:${d.threadConvID}` : d.kind === "agent" ? `a:${d.memberID}` : `m:${d.convID}`;
}

function viewKey(v: MainView) {
  return v.kind === "conversation" ? `c:${v.convID}` : v.kind === "admin" ? "admin" : v.kind;
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
  const dm = useAction();
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
    const invalidate = () => {
      for (const k of ["bootstrap", "inbox", "tasks", "messages", "task", "conv-members"]) void qc.invalidateQueries({ queryKey: [k, spaceSlug] });
    };
    for (const ev of SSE_EVENTS) es.addEventListener(ev, invalidate);
    es.addEventListener("space.updated", () => void qc.invalidateQueries({ queryKey: ["me"] }));
    return () => es.close();
  }, [spaceSlug, qc]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setDialog((d) => (d === "jump" ? null : "jump"));
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const activeConvID = mainView.kind === "conversation" ? mainView.convID : "";
  const activeConv = useMemo(() => boot?.conversations.find((c) => c.id === activeConvID), [boot, activeConvID]);

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

  const memberRow = (m: Member) => {
    const conv = dms.find((d) => d.peer_member_id === m.id);
    const active = Boolean(conv && activeConvID === conv.id);
    const st = m.agent?.runtime_state;
    const sub = m.kind === "agent" && (st === "working" || st === "error") ? agentStatusText(boot, m) : "";
    return (
      <button
        key={m.id}
        onClick={() => void openOrCreateDM(m.id)}
        aria-current={active ? "page" : undefined}
        className={`w-full px-2 ${sub ? "py-1.5" : "h-9"} rounded-[8px] flex items-center gap-2.5 text-left transition-colors duration-100 ${
          active ? "bg-nav-active" : "hover:bg-nav-hover"
        }`}
      >
        <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" state={m.kind === "agent" ? st : undefined} ring="var(--nav)" />
        <span className="min-w-0 flex-1">
          <span className={`block truncate text-[14px] leading-5 ${active || (conv && conv.unread_count > 0) ? "text-nav-fg-strong font-semibold" : "text-nav-fg-2"}`}>{m.display_name}</span>
          {sub && <span className={`block truncate text-[12px] leading-4 ${st === "error" ? "text-nav-danger" : "text-nav-busy"}`}>{sub}</span>}
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

          <div>
            <SectionLabel action={<SectionAdd label="New channel" onClick={() => setDialog("channel")} />}>Channels</SectionLabel>
            <div className="space-y-0.5">
              {channels.map((c) => {
                const muted = c.notify_level === "mute";
                return (
                  <NavItem
                    key={c.id}
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
                );
              })}
            </div>
          </div>

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
        <div key={viewKey(mainView)} className="h-full min-h-0 animate-fade-in">
          {mainView.kind === "conversation" && activeConv && <ConversationView spaceSlug={spaceSlug} boot={boot} conv={activeConv} onOpenDetail={setDetail} />}
          {mainView.kind === "inbox" && (
            <InboxView
              spaceSlug={spaceSlug}
              filter={inboxFilter}
              onFilter={setInboxFilter}
              onOpenConversation={(id) => setMainView({ kind: "conversation", convID: id })}
              onOpenDetail={setDetail}
            />
          )}
          {mainView.kind === "tasks" && <TasksView spaceSlug={spaceSlug} filter={taskFilter} onFilter={setTaskFilter} onOpenDetail={setDetail} />}
          {mainView.kind === "admin" && (
            <AdminPanel
              boot={boot}
              tab={mainView.tab}
              onTab={(tab) => setMainView({ kind: "admin", tab })}
              onInspectAgent={(id) => setDetail({ kind: "agent", memberID: id })}
              selectedAgentID={detail?.kind === "agent" ? detail.memberID : undefined}
              onNewAgent={() => setDialog(myComputers.length ? "agent" : "computer")}
              onConnectComputer={() => setDialog("computer")}
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
            />
          </div>
        )}
      </div>

      <NewChannelDialog
        open={dialog === "channel"}
        spaceSlug={spaceSlug}
        onClose={() => setDialog(null)}
        onCreated={(id) => {
          setDialog(null);
          setMainView({ kind: "conversation", convID: id });
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
          ...channels.map((c) => ({ key: c.id, label: c.slug ?? "", group: "Channels", icon: <Hash className="size-4" />, run: () => setMainView({ kind: "conversation", convID: c.id }) })),
          ...[...agents, ...people].map((m) => ({
            key: m.id,
            label: m.display_name,
            group: m.kind === "agent" ? "Agents" : "People",
            icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" />,
            run: () => void openOrCreateDM(m.id),
          })),
          ...boot.tasks.map((t) => ({
            key: t.id,
            label: `#${t.number} ${t.title}`,
            group: "Tasks",
            icon: <CheckCircle2 className="size-4" />,
            run: () => setDetail({ kind: "task", taskID: t.id }),
          })),
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

function NewChannelDialog({ open, spaceSlug, onClose, onCreated }: { open: boolean; spaceSlug: string; onClose: () => void; onCreated: (convID: string) => void }) {
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [topic, setTopic] = useState("");
  const [kind, setKind] = useState<"public" | "private">("public");
  const { busy, run } = useAction();
  useEffect(() => {
    if (open) {
      setSlug("");
      setTopic("");
      setKind("public");
    }
  }, [open]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const r = await api.createChannel(spaceSlug, slugify(slug), kind, topic.trim());
      await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      onCreated(r.conversation.id);
    });
  }

  return (
    <Modal open={open} onClose={onClose} label="New channel" title="New channel" description="Channels are where people and agents work in the open.">
      <form onSubmit={submit} className="px-6 pb-6 pt-4 space-y-4">
        <Field label="Name" autoFocus leading={<Hash />} value={slug} onChange={(e) => setSlug(slugInput(e.target.value))} placeholder="design-review" required />
        <Field label="Topic" optional value={topic} onChange={(e) => setTopic(e.target.value)} placeholder="What is this channel about?" />
        <div className="space-y-1.5">
          <div className="text-[13px] font-medium text-fg-2">Visibility</div>
          <Segmented
            full
            value={kind}
            onChange={setKind}
            options={[
              { value: "public", label: <><Hash />Public</> },
              { value: "private", label: <><Lock />Private</> },
            ]}
          />
        </div>
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!slug}>
            Create channel
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function NewSpaceDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (slug: string) => void }) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [touched, setTouched] = useState(false);
  const { busy, run } = useAction();
  useEffect(() => {
    if (open) {
      setName("");
      setSlug("");
      setTouched(false);
    }
  }, [open]);
  return (
    <Modal open={open} onClose={onClose} label="New space" title="New space" description="A separate home for channels, tasks, agents and computers.">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void run(async () => {
            const r = await api.createSpace(slugify(slug), name.trim());
            onCreated(r.space.slug);
          });
        }}
        className="px-6 pb-6 pt-4 space-y-4"
      >
        <Field
          label="Name"
          autoFocus
          value={name}
          onChange={(e) => {
            setName(e.target.value);
            if (!touched) setSlug(slugify(e.target.value));
          }}
          required
        />
        <Field
          label="Address"
          value={slug}
          className="font-mono"
          onChange={(e) => {
            setTouched(true);
            setSlug(slugInput(e.target.value));
          }}
          required
        />
        <div className="flex justify-end gap-2 pt-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!slug || !name.trim()}>
            Create space
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function JumpDialog({
  open,
  onClose,
  items,
}: {
  open: boolean;
  onClose: () => void;
  items: { key: string; label: string; group: string; icon: ReactNode; run: () => void }[];
}) {
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
    }
  }, [open]);
  const filtered = items.filter((i) => i.label.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 14);
  const pick = (i: number) => {
    const it = filtered[i];
    if (!it) return;
    onClose();
    it.run();
  };
  return (
    <Modal open={open} onClose={onClose} label="Jump to" width={560}>
      <div className="border-b border-line flex items-center gap-3 px-4 h-14">
        <Search className="size-5 text-fg-3" />
        <input
          autoFocus
          value={q}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(s + 1, filtered.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(s - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              pick(sel);
            }
          }}
          placeholder="Search channels, agents, people and tasks"
          className="flex-1 h-full text-[15px] bg-transparent outline-none placeholder:text-fg-4 pr-8"
        />
      </div>
      <div className="max-h-[380px] overflow-y-auto scroll-thin p-2">
        {filtered.length === 0 ? (
          <div className="py-10 text-center text-[14px] text-fg-3">No matches</div>
        ) : (
          filtered.map((it, i) => (
            <div key={it.key}>
              {(i === 0 || filtered[i - 1].group !== it.group) && <div className="px-2.5 pt-2 pb-1 text-[12px] font-semibold text-fg-4">{it.group}</div>}
              <button
                onMouseEnter={() => setSel(i)}
                onClick={() => pick(i)}
                className={`w-full h-10 px-2.5 rounded-[9px] flex items-center gap-3 text-[14px] text-left ${i === sel ? "bg-accent-soft text-fg" : "text-fg-2"}`}
              >
                <span className="size-5 flex items-center justify-center text-fg-3">{it.icon}</span>
                <span className="truncate">{it.label}</span>
              </button>
            </div>
          ))
        )}
      </div>
    </Modal>
  );
}
