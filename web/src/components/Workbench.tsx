import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Hash, Inbox, Lock, Monitor, Plus, Search, Settings, Sparkles, Users } from "lucide-react";
import { api, slugInput, slugify, type Space } from "../api";
import { readPendingPairing } from "../pairing";
import { ConversationView } from "./ConversationView";
import { DetailPane, type DetailTarget } from "./DetailPane";
import { MemberAvatar, RuntimeDot } from "./Identity";
import { AgentStep, ComputerStep, PairingCard } from "./Setup";
import { WorkbenchSkeleton } from "./Skeletons";
import { Button, Field, Modal, Segmented, useAction } from "./ui";
import { AdminPanel, InboxView, TasksView, type AdminTab, type InboxFilter, type TaskFilter } from "./Views";

type MainView =
  | { kind: "conversation"; convID: string }
  | { kind: "inbox" }
  | { kind: "tasks" }
  | { kind: "admin"; tab: AdminTab };

type Dialog = null | "channel" | "space" | "jump" | { setup: "computer" | "agent" } | "pairing";

const SSE_EVENTS = [
  "message.created",
  "task.created",
  "task.updated",
  "agent.created",
  "agent.state_changed",
  "computer.changed",
  "conversation.created",
  "member.permissions_changed",
];

function viewKey(v: MainView) {
  return v.kind === "conversation" ? `c:${v.convID}` : v.kind === "admin" ? "admin" : v.kind;
}

function NavRow({
  active,
  onClick,
  children,
  trailing,
  dim,
  bold,
  label,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
  trailing?: ReactNode;
  dim?: boolean;
  bold?: boolean;
  label?: string;
}) {
  return (
    <button
      onClick={onClick}
      aria-label={label}
      aria-current={active ? "page" : undefined}
      className={`w-full h-7 px-2 rounded-[5px] flex items-center justify-between gap-2 text-[13px] transition-colors duration-100 ${
        active
          ? "bg-active text-ink font-semibold"
          : dim
            ? "text-faint hover:bg-hover"
            : bold
              ? "text-ink font-semibold hover:bg-hover"
              : "text-ink-2 hover:bg-hover"
      }`}
    >
      <span className="flex items-center gap-2 min-w-0">{children}</span>
      {trailing}
    </button>
  );
}

function Count({ n, warm = true }: { n: number; warm?: boolean }) {
  return (
    <span
      className={`min-w-[18px] h-[18px] px-1 rounded-[4px] text-[11px] font-semibold font-mono-num inline-flex items-center justify-center animate-pop-in ${
        warm ? "bg-warm-bg text-warm" : "text-mute"
      }`}
    >
      {n}
    </span>
  );
}

function SectionLabel({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="px-2 mb-1 h-5 flex items-center justify-between text-[11.5px] font-medium text-mute">
      <span>{children}</span>
      {action}
    </div>
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
  const bootQ = useQuery({
    queryKey: ["bootstrap", spaceSlug],
    queryFn: () => api.bootstrap(spaceSlug),
  });

  const [mainView, setMainView] = useState<MainView>({ kind: "inbox" });
  const [detail, setDetail] = useState<DetailTarget | null>(null);
  const [inboxFilter, setInboxFilter] = useState<InboxFilter>("needs_me");
  const [taskFilter, setTaskFilter] = useState<TaskFilter>("active");
  const [dialog, setDialog] = useState<Dialog>(() => (readPendingPairing() ? "pairing" : null));
  const dm = useAction();
  const landed = useRef(false);

  const boot = bootQ.data;

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

  // Single SSE connection per Space for live updates.
  useEffect(() => {
    const es = new EventSource(`/api/v1/spaces/${spaceSlug}/events`, { withCredentials: true });
    const invalidate = () => {
      void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["inbox", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["tasks", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["messages", spaceSlug] });
      void qc.invalidateQueries({ queryKey: ["task", spaceSlug] });
    };
    for (const ev of SSE_EVENTS) es.addEventListener(ev, invalidate);
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
    const existing = boot?.conversations.find((c) => c.kind === "dm" && c.peer_member_id === peerMemberID);
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
  const onlineComputers = boot.computers.filter((c) => c.connection_status === "online").length;
  const canGovern = boot.current_member.access_level !== "member";
  const needsSetup = canGovern && (boot.computers.length === 0 || agents.length === 0);
  const needsMe = boot.inbox.filter((i) => i.needs_attention).length;
  const peers = boot.members.filter((m) => m.id !== boot.current_member.id);
  const pairing = readPendingPairing();

  return (
    <div
      className="h-full grid bg-white transition-[grid-template-columns] duration-200 ease-out"
      style={{ gridTemplateColumns: detail ? "44px 228px minmax(0, 1fr) 388px" : "44px 228px minmax(0, 1fr) 0px" }}
    >
      {/* Anchor rail */}
      <nav aria-label="Spaces" className="bg-rail border-r border-line-strong flex flex-col items-center justify-between py-2 select-none">
        <div className="flex flex-col items-center gap-1.5">
          {(spaces.length > 0 ? spaces : [boot.space]).map((sp) => {
            const active = sp.slug === spaceSlug;
            return (
              <button
                key={sp.id}
                onClick={() => (active ? setMainView({ kind: "inbox" }) : onSwitchSpace?.(sp.slug))}
                title={sp.name}
                aria-label={`Space ${sp.name}`}
                className="relative w-7 h-7 flex items-center justify-center"
              >
                {active && <span className="absolute -left-2 w-[3px] h-5 rounded-r bg-ink animate-fade-in" />}
                <span
                  className={`w-7 h-7 rounded-[7px] text-[12px] font-semibold flex items-center justify-center transition-colors ${
                    active ? "bg-ink text-white" : "bg-[#DEDBD2] text-ink-2 hover:bg-[#D3CFC5]"
                  }`}
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
            className="w-7 h-7 rounded-[7px] border border-dashed border-[#C9C5BB] text-mute hover:text-ink hover:border-ink flex items-center justify-center transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
          </button>
        </div>
        <div className="flex flex-col items-center gap-1">
          {[
            { tab: "computers" as const, icon: <Monitor className="w-4 h-4" />, title: `Computers (${onlineComputers} online)` },
            { tab: "members" as const, icon: <Users className="w-4 h-4" />, title: "Members & Permissions" },
            { tab: "settings" as const, icon: <Settings className="w-4 h-4" />, title: "Space Settings" },
          ].map((b) => {
            const active = mainView.kind === "admin" && mainView.tab === b.tab;
            return (
              <button
                key={b.tab}
                onClick={() => setMainView({ kind: "admin", tab: b.tab })}
                title={b.title}
                aria-label={b.title}
                className={`w-7 h-7 rounded-[6px] flex items-center justify-center relative transition-colors ${
                  active ? "bg-[#DEDBD2] text-ink" : "text-mute hover:bg-[#E0DDD4] hover:text-ink"
                }`}
              >
                {b.icon}
                {b.tab === "computers" && (
                  <span
                    className={`w-1.5 h-1.5 rounded-full absolute top-1 right-1 ${onlineComputers > 0 ? "bg-work" : "bg-[#BDB9AE]"}`}
                  />
                )}
              </button>
            );
          })}
        </div>
      </nav>

      {/* Sidebar */}
      <aside aria-label="Sidebar" className="bg-paper border-r border-line-strong flex flex-col min-h-0 select-none">
        <div className="h-11 px-3 border-b border-line flex items-center justify-between gap-2 shrink-0">
          <span className="text-[13.5px] font-semibold text-ink truncate">{boot.space.name}</span>
          <button
            onClick={() => setDialog("jump")}
            title="Jump to… (⌘K)"
            className="h-6 px-1.5 rounded-[5px] border border-line bg-white/60 text-mute hover:text-ink hover:border-field flex items-center gap-1 transition-colors shrink-0"
          >
            <Search className="w-3 h-3" />
            <span className="font-mono-num text-[10.5px]">⌘K</span>
          </button>
        </div>

        <div className="flex-1 min-h-0 p-2 space-y-4 overflow-y-auto scroll-thin">
          <div className="space-y-0.5">
            <NavRow
              active={mainView.kind === "inbox"}
              onClick={() => setMainView({ kind: "inbox" })}
              trailing={needsMe > 0 ? <Count n={needsMe} /> : boot.inbox.length > 0 ? <Count n={boot.inbox.length} warm={false} /> : null}
            >
              <Inbox className="w-3.5 h-3.5 text-mute" />
              <span>Inbox</span>
            </NavRow>
            <NavRow
              active={mainView.kind === "tasks"}
              onClick={() => setMainView({ kind: "tasks" })}
              trailing={<span className="text-[11px] font-mono-num text-faint">{boot.tasks.length || ""}</span>}
            >
              <CheckCircle2 className="w-3.5 h-3.5 text-mute" />
              <span>Tasks</span>
            </NavRow>
          </div>

          <div>
            <SectionLabel
              action={
                <button onClick={() => setDialog("channel")} title="New channel" aria-label="New channel" className="w-5 h-5 rounded hover:bg-hover hover:text-ink flex items-center justify-center">
                  <Plus className="w-3.5 h-3.5" />
                </button>
              }
            >
              Channels
            </SectionLabel>
            <div className="space-y-0.5">
              {channels.map((c) => {
                const muted = c.notify_level === "mute";
                return (
                  <NavRow
                    key={c.id}
                    active={activeConvID === c.id}
                    onClick={() => setMainView({ kind: "conversation", convID: c.id })}
                    dim={muted}
                    bold={c.unread_count > 0 && !muted}
                    trailing={
                      c.mention_count > 0 ? (
                        <Count n={c.mention_count} />
                      ) : c.unread_count > 0 && !muted ? (
                        <span className="w-1.5 h-1.5 rounded-full bg-ink shrink-0" />
                      ) : null
                    }
                  >
                    {c.kind === "private" ? <Lock className="w-3 h-3 text-mute shrink-0" /> : <Hash className="w-3 h-3 text-mute shrink-0" />}
                    <span className="truncate">{c.slug}</span>
                  </NavRow>
                );
              })}
            </div>
          </div>

          <div>
            <SectionLabel>Direct messages</SectionLabel>
            <div className="space-y-0.5">
              {peers.map((m) => {
                const conv = dms.find((d) => d.peer_member_id === m.id);
                return (
                  <NavRow
                    key={m.id}
                    active={Boolean(conv && activeConvID === conv.id)}
                    onClick={() => void openOrCreateDM(m.id)}
                    bold={Boolean(conv && conv.unread_count > 0)}
                    trailing={
                      conv && conv.unread_count > 0 ? (
                        <Count n={conv.unread_count} />
                      ) : m.kind === "agent" ? (
                        <span className={m.agent?.runtime_state === "working" ? "animate-breathe" : ""}>
                          <RuntimeDot state={m.agent?.runtime_state} />
                        </span>
                      ) : null
                    }
                  >
                    <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" />
                    <span className="truncate">{m.display_name}</span>
                  </NavRow>
                );
              })}
              {peers.length === 0 && <p className="px-2 py-1 text-[12px] text-faint">Create an agent to start a DM.</p>}
            </div>
          </div>
        </div>

        {needsSetup && (
          <div className="px-2 pb-2 shrink-0 animate-fade-in">
            <button
              onClick={() => setDialog({ setup: boot.computers.length === 0 ? "computer" : "agent" })}
              className="w-full p-2.5 rounded-[7px] border border-line bg-white hover:border-field text-left transition-colors"
            >
              <div className="flex items-center gap-1.5 text-[12.5px] font-semibold text-ink">
                <Sparkles className="w-3.5 h-3.5" /> Finish setup
              </div>
              <div className="mt-1.5 space-y-1 text-[11.5px]">
                <SetupLine done={boot.computers.length > 0}>Connect a computer</SetupLine>
                <SetupLine done={agents.length > 0}>Create an agent</SetupLine>
              </div>
            </button>
          </div>
        )}

        <div className="h-10 px-3 border-t border-line flex items-center justify-between text-[12.5px] shrink-0">
          <button
            onClick={() => setMainView({ kind: "admin", tab: "settings" })}
            className="flex items-center gap-2 min-w-0 hover:text-ink"
          >
            <MemberAvatar id={boot.current_member.id} name={boot.current_member.display_name} kind="human" size="sm" />
            <span className="font-medium text-ink truncate">{boot.current_member.display_name}</span>
          </button>
          <span className="text-[11px] text-faint capitalize">{boot.current_member.access_level}</span>
        </div>
      </aside>

      {/* Main pane */}
      <main className="bg-white flex flex-col min-w-0 min-h-0">
        <div key={viewKey(mainView)} className="h-full min-h-0 animate-fade-in">
          {mainView.kind === "conversation" && activeConv && (
            <ConversationView spaceSlug={spaceSlug} boot={boot} conv={activeConv} onOpenDetail={setDetail} />
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
          {mainView.kind === "tasks" && (
            <TasksView spaceSlug={spaceSlug} filter={taskFilter} onFilter={setTaskFilter} onOpenDetail={setDetail} />
          )}
          {mainView.kind === "admin" && (
            <AdminPanel
              spaceSlug={spaceSlug}
              boot={boot}
              tab={mainView.tab}
              onTab={(tab) => setMainView({ kind: "admin", tab })}
              onInspectAgent={(id) => setDetail({ kind: "agent", memberID: id })}
              onOpenSetup={(step) => setDialog({ setup: step })}
              onLogout={onLogout}
            />
          )}
        </div>
      </main>

      {/* Right detail pane */}
      <div className="min-w-0 min-h-0 overflow-hidden">
        {detail && (
          <div key={JSON.stringify(detail)} className="h-full w-[388px] animate-slide-in">
            <DetailPane spaceSlug={spaceSlug} boot={boot} target={detail} onClose={() => setDetail(null)} onSelectTarget={setDetail} />
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
      <Modal open={typeof dialog === "object" && dialog !== null} onClose={() => setDialog(null)} label="Finish setup" width={460}>
        <div className="p-6">
          {typeof dialog === "object" && dialog?.setup === "computer" && (
            <ComputerStep spaceSlug={spaceSlug} onNext={() => setDialog(agents.length === 0 ? { setup: "agent" } : null)} />
          )}
          {typeof dialog === "object" && dialog?.setup === "agent" && (
            <AgentStep
              spaceSlug={spaceSlug}
              onSkip={() => setDialog(null)}
              onDone={(id) => {
                setDialog(null);
                void qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] }).then(() => openOrCreateDM(id));
              }}
            />
          )}
        </div>
      </Modal>
      <Modal open={dialog === "pairing" && Boolean(pairing)} onClose={() => setDialog(null)} label="Connect this computer">
        <div className="p-6 space-y-4">
          <div>
            <h2 className="text-[15px] font-semibold">Connect this computer to {boot.space.name}?</h2>
            <p className="mt-0.5 text-[12.5px] text-mute">The Sumi desktop app on this machine asked to join.</p>
          </div>
          {pairing && canGovern ? (
            <PairingCard spaceSlug={spaceSlug} pairing={pairing} onConfirmed={() => setDialog(null)} onDismiss={() => setDialog(null)} />
          ) : (
            <p className="text-[12.5px] text-mute">Only owners and admins can connect computers.</p>
          )}
        </div>
      </Modal>
      <JumpDialog
        open={dialog === "jump"}
        onClose={() => setDialog(null)}
        items={[
          { key: "inbox", label: "Inbox", icon: <Inbox className="w-3.5 h-3.5" />, run: () => setMainView({ kind: "inbox" }) },
          { key: "tasks", label: "Tasks", icon: <CheckCircle2 className="w-3.5 h-3.5" />, run: () => setMainView({ kind: "tasks" }) },
          ...channels.map((c) => ({
            key: c.id,
            label: `#${c.slug}`,
            icon: <Hash className="w-3.5 h-3.5" />,
            run: () => setMainView({ kind: "conversation", convID: c.id }),
          })),
          ...peers.map((m) => ({
            key: m.id,
            label: m.display_name,
            icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="sm" />,
            run: () => void openOrCreateDM(m.id),
          })),
          ...boot.tasks.map((t) => ({
            key: t.id,
            label: `#${t.number} ${t.title}`,
            icon: <CheckCircle2 className="w-3.5 h-3.5" />,
            run: () => setDetail({ kind: "task", taskID: t.id }),
          })),
        ]}
      />
    </div>
  );
}

function SetupLine({ done, children }: { done: boolean; children: ReactNode }) {
  return (
    <div className={`flex items-center gap-1.5 ${done ? "text-faint line-through" : "text-ink-2"}`}>
      <span className={`w-3 h-3 rounded-full flex items-center justify-center ${done ? "bg-ink text-white" : "border border-field"}`}>
        {done && <span className="text-[8px]">✓</span>}
      </span>
      {children}
    </div>
  );
}

function NewChannelDialog({
  open,
  spaceSlug,
  onClose,
  onCreated,
}: {
  open: boolean;
  spaceSlug: string;
  onClose: () => void;
  onCreated: (convID: string) => void;
}) {
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
    <Modal open={open} onClose={onClose} label="New channel">
      <form onSubmit={submit} className="p-6 space-y-4">
        <h2 className="text-[15px] font-semibold">New channel</h2>
        <Field
          label="Name"
          autoFocus
          value={slug}
          onChange={(e) => setSlug(slugInput(e.target.value))}
          placeholder="design-review"
          hint="Lowercase letters, digits and hyphens."
          required
        />
        <Field label="Topic" value={topic} onChange={(e) => setTopic(e.target.value)} placeholder="Optional" />
        <Segmented
          size="md"
          value={kind}
          onChange={setKind}
          options={[
            { value: "public", label: "Public" },
            { value: "private", label: "Private" },
          ]}
        />
        <div className="flex justify-end gap-2 pt-1">
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
    <Modal open={open} onClose={onClose} label="New space">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void run(async () => {
            const r = await api.createSpace(slugify(slug), name.trim());
            onCreated(r.space.slug);
          });
        }}
        className="p-6 space-y-4"
      >
        <h2 className="text-[15px] font-semibold">New space</h2>
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
          className="font-mono-num"
          onChange={(e) => {
            setTouched(true);
            setSlug(slugInput(e.target.value));
          }}
          required
        />
        <div className="flex justify-end gap-2 pt-1">
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
  items: { key: string; label: string; icon: ReactNode; run: () => void }[];
}) {
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
    }
  }, [open]);
  const filtered = items.filter((i) => i.label.toLowerCase().includes(q.trim().toLowerCase())).slice(0, 12);
  const pick = (i: number) => {
    const it = filtered[i];
    if (!it) return;
    onClose();
    it.run();
  };
  return (
    <Modal open={open} onClose={onClose} label="Jump to" width={480}>
      <div className="border-b border-line flex items-center gap-2 px-3.5 h-11">
        <Search className="w-4 h-4 text-mute" />
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
          placeholder="Jump to a channel, person or task…"
          className="flex-1 h-full text-[13.5px] outline-none placeholder:text-faint pr-6"
        />
      </div>
      <div className="max-h-[320px] overflow-y-auto scroll-thin p-1">
        {filtered.length === 0 ? (
          <div className="py-8 text-center text-[12.5px] text-faint">No matches</div>
        ) : (
          filtered.map((it, i) => (
            <button
              key={it.key}
              onMouseEnter={() => setSel(i)}
              onClick={() => pick(i)}
              className={`w-full h-8 px-2.5 rounded-[6px] flex items-center gap-2.5 text-[13px] text-left ${
                i === sel ? "bg-paper text-ink" : "text-ink-2"
              }`}
            >
              <span className="w-4 flex justify-center text-mute">{it.icon}</span>
              <span className="truncate">{it.label}</span>
            </button>
          ))
        )}
      </div>
    </Modal>
  );
}
