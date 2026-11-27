import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { Hash, Keyboard, Loader2, Lock, MessageSquare, Search } from "lucide-react";
import { api, formatClock, slugInput, slugify, type ChannelGroup, type Member, type SearchHit } from "../api";
import { isMacPlatform, useSpellCheckAttr } from "../preferences";
import { MemberAvatar } from "./Avatar";
import { Button, Field, Modal, MultiSelectList, Segmented, useAction } from "./ui";

export interface JumpItem {
  key: string;
  label: string;
  keywords?: string;
  group: string;
  icon: ReactNode;
  shortcut?: string;
  run: () => void;
}

/** Subsequence fuzzy score: higher is better, -1 means no match. */
function fuzzyScore(haystack: string, needle: string): number {
  if (!needle) return 0;
  const h = haystack.toLowerCase();
  const n = needle.toLowerCase();
  const idx = h.indexOf(n);
  if (idx === 0) return 1000 - h.length;
  if (idx > 0) return 600 - idx;
  let hi = 0;
  let score = 0;
  let streak = 0;
  for (let ni = 0; ni < n.length; ni++) {
    const found = h.indexOf(n[ni], hi);
    if (found < 0) return -1;
    streak = found === hi ? streak + 1 : 0;
    score += 10 + streak * 5 - (found - hi);
    hi = found + 1;
  }
  return score;
}

export function NewChannelDialog({
  open,
  spaceSlug,
  members,
  currentMemberID,
  onClose,
  onCreated,
}: {
  open: boolean;
  spaceSlug: string;
  members: Member[];
  currentMemberID: string;
  onClose: () => void;
  onCreated: (convID: string) => void;
}) {
  const qc = useQueryClient();
  const [slug, setSlug] = useState("");
  const [topic, setTopic] = useState("");
  const [kind, setKind] = useState<"public" | "private">("public");
  const [selectedMembers, setSelectedMembers] = useState<string[]>([]);
  const { busy, run } = useAction();
  useEffect(() => {
    if (open) {
      setSlug("");
      setTopic("");
      setKind("public");
      setSelectedMembers([]);
    }
  }, [open]);

  const candidates = useMemo(
    () =>
      members
        .filter((m) => m.id !== currentMemberID)
        .sort((a, b) => (a.kind === b.kind ? a.display_name.localeCompare(b.display_name) : a.kind === "agent" ? -1 : 1)),
    [members, currentMemberID],
  );

  async function submit(e: FormEvent) {
    e.preventDefault();
    await run(async () => {
      const r = await api.createChannel(spaceSlug, slugify(slug), kind, topic.trim(), selectedMembers);
      await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      onCreated(r.conversation.id);
    });
  }

  return (
    <Modal open={open} onClose={onClose} label="New channel" title="New channel" description="Start with only the agents and people you invite; you can add more anytime.">
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
        {candidates.length > 0 && (
          <MultiSelectList
            label={selectedMembers.length > 0 ? `Initial members (${selectedMembers.length})` : "Initial members"}
            optional
            hint="Leave empty to start alone. Agents only see and wake in channels they are added to."
            values={selectedMembers}
            onChange={setSelectedMembers}
            searchPlaceholder="Filter agents and people…"
            options={candidates.map((m) => ({
              value: m.id,
              label: m.display_name,
              description: m.kind === "agent" ? (m.agent?.role_text ?? "").replace(/\s+/g, " ").trim() : undefined,
              icon: <MemberAvatar id={m.id} name={m.display_name} kind={m.kind} size="xs" state={m.kind === "agent" ? m.agent?.runtime_state : undefined} />,
              trailing: m.kind === "agent" ? "Agent" : m.access_level,
            }))}
          />
        )}
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

export function ChannelGroupDialog({
  open,
  spaceSlug,
  group,
  onClose,
}: {
  open: boolean;
  spaceSlug: string;
  group: ChannelGroup | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const { busy, run } = useAction();

  useEffect(() => {
    if (open) setName(group?.name ?? "");
  }, [open, group]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const clean = name.trim();
    if (!clean) return;
    await run(async () => {
      if (group) await api.updateChannelGroup(spaceSlug, group.id, { name: clean });
      else await api.createChannelGroup(spaceSlug, clean);
      await qc.invalidateQueries({ queryKey: ["bootstrap", spaceSlug] });
      onClose();
    });
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      label={group ? "Rename channel group" : "New channel group"}
      title={group ? "Rename group" : "New channel group"}
      description="Organize your sidebar channels into collapsible groups by dragging them in."
      width={420}
    >
      <form onSubmit={submit} className="px-6 pb-6 pt-4 space-y-4">
        <Field label="Group name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="Core projects" maxLength={40} required />
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" loading={busy} disabled={!name.trim()}>
            {group ? "Save" : "Create group"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

export function NewSpaceDialog({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (slug: string) => void }) {
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

function HighlightText({ text, query }: { text: string; query: string }) {
  const q = query.trim();
  if (!q) return <>{text}</>;
  const lower = text.toLowerCase();
  const idx = lower.indexOf(q.toLowerCase());
  if (idx < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, idx)}
      <mark className="bg-accent-soft text-accent-text rounded-[2px] px-0.5 font-medium">{text.slice(idx, idx + q.length)}</mark>
      {text.slice(idx + q.length)}
    </>
  );
}

export function JumpDialog({
  open,
  onClose,
  items,
  spaceSlug,
  onSelectMessageHit,
}: {
  open: boolean;
  onClose: () => void;
  items: JumpItem[];
  spaceSlug?: string;
  onSelectMessageHit?: (hit: SearchHit) => void;
}) {
  const [q, setQ] = useState("");
  const spellCheck = useSpellCheckAttr();
  const [sel, setSel] = useState(0);
  const [hits, setHits] = useState<SearchHit[]>([]);
  const [searching, setSearching] = useState(false);
  const rowRefs = useRef<(HTMLButtonElement | null)[]>([]);

  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
      setHits([]);
      setSearching(false);
    }
  }, [open]);

  useEffect(() => {
    const term = q.trim();
    if (!open || !spaceSlug || !onSelectMessageHit || term.length < 2) {
      setHits([]);
      setSearching(false);
      return;
    }
    let cancelled = false;
    setSearching(true);
    const timer = window.setTimeout(() => {
      void api
        .searchMessages(spaceSlug, term, { limit: 8 })
        .then((res) => {
          if (!cancelled) setHits(res.hits ?? []);
        })
        .catch(() => {
          if (!cancelled) setHits([]);
        })
        .finally(() => {
          if (!cancelled) setSearching(false);
        });
    }, 180);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [open, q, spaceSlug, onSelectMessageHit]);

  const filtered = useMemo(() => {
    const term = q.trim();
    if (!term) return items.slice(0, 16);
    return items
      .map((it) => ({
        it,
        score: Math.max(fuzzyScore(it.label, term), it.keywords ? fuzzyScore(it.keywords, term) - 100 : -1),
      }))
      .filter((x) => x.score >= 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 12)
      .map((x) => x.it);
  }, [items, q]);

  const totalCount = filtered.length + hits.length;

  useEffect(() => {
    if (!open) return;
    setSel((s) => (totalCount === 0 ? 0 : Math.min(s, totalCount - 1)));
  }, [open, totalCount]);

  useEffect(() => {
    if (!open) return;
    rowRefs.current[sel]?.scrollIntoView?.({ block: "nearest" });
  }, [open, sel]);

  const pick = (i: number) => {
    if (i < filtered.length) {
      const it = filtered[i];
      if (!it) return;
      onClose();
      it.run();
      return;
    }
    const hit = hits[i - filtered.length];
    if (!hit) return;
    onClose();
    onSelectMessageHit?.(hit);
  };

  return (
    <Modal open={open} onClose={onClose} label="Command palette" width={580}>
      <div className="border-b border-line flex items-center gap-3 px-4 h-14">
        {searching ? <Loader2 className="size-4.5 text-fg-3 shrink-0 animate-spin" /> : <Search className="size-4.5 text-fg-3 shrink-0" />}
        <input
          autoFocus
          value={q}
          spellCheck={spellCheck}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => (totalCount ? (s + 1) % totalCount : 0));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => (totalCount ? (s - 1 + totalCount) % totalCount : 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              pick(sel);
            }
          }}
          placeholder="Search channels, agents, tasks, or messages…"
          className="flex-1 h-full text-[15px] bg-transparent outline-none placeholder:text-fg-4 pr-8"
        />
      </div>
      <div className="max-h-[400px] overflow-y-auto scroll-thin p-2">
        {totalCount === 0 && !searching ? (
          <div className="py-10 text-center text-[14px] text-fg-3">No matches</div>
        ) : (
          <>
            {filtered.map((it, i) => (
              <div key={it.key}>
                {(i === 0 || filtered[i - 1].group !== it.group) && (
                  <div className="px-2.5 pt-2 pb-1 text-[11.5px] font-semibold uppercase tracking-[0.04em] text-fg-4">{it.group}</div>
                )}
                <button
                  ref={(el) => {
                    rowRefs.current[i] = el;
                  }}
                  type="button"
                  onMouseEnter={() => setSel(i)}
                  onClick={() => pick(i)}
                  className={`w-full h-10 px-2.5 rounded-[6px] flex items-center gap-3 text-[14px] text-left transition-colors ${
                    i === sel ? "bg-accent-soft text-fg" : "text-fg-2 hover:bg-hover"
                  }`}
                >
                  <span className="size-5 flex items-center justify-center text-fg-3 shrink-0">{it.icon}</span>
                  <span className="truncate flex-1">{it.label}</span>
                  {it.shortcut && (
                    <kbd className="text-[11px] font-mono px-1.5 py-0.5 rounded-[4px] bg-subtle border border-line text-fg-3">{it.shortcut}</kbd>
                  )}
                </button>
              </div>
            ))}
            {hits.length > 0 && (
              <div>
                <div className="px-2.5 pt-2.5 pb-1 text-[11.5px] font-semibold uppercase tracking-[0.04em] text-fg-4">Messages</div>
                {hits.map((h, idx) => {
                  const rowIdx = filtered.length + idx;
                  return (
                    <button
                      key={h.message_id}
                      ref={(el) => {
                        rowRefs.current[rowIdx] = el;
                      }}
                      type="button"
                      onMouseEnter={() => setSel(rowIdx)}
                      onClick={() => pick(rowIdx)}
                      className={`w-full px-2.5 py-2 rounded-[6px] flex items-start gap-2.5 text-left transition-colors ${
                        rowIdx === sel ? "bg-accent-soft text-fg" : "text-fg-2 hover:bg-hover"
                      }`}
                    >
                      <span className="size-5 mt-0.5 flex items-center justify-center text-fg-3 shrink-0">
                        <MessageSquare className="size-3.5" />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="flex items-center gap-1.5 text-[12px] text-fg-3">
                          <span className="font-semibold text-fg">{h.author_display_name}</span>
                          <span>in</span>
                          <span className="font-medium text-fg-2 truncate">{h.conversation_title}</span>
                          <span className="ml-auto tabular text-[11px] text-fg-4 shrink-0">{formatClock(h.created_at)}</span>
                        </span>
                        <span className="mt-0.5 block text-[13px] text-fg-2 truncate font-body">
                          <HighlightText text={h.snippet} query={q} />
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            )}
          </>
        )}
      </div>
      <div className="px-4 py-2 border-t border-line bg-subtle/40 flex items-center justify-between text-[11.5px] text-fg-4">
        <span>↑↓ Navigate · ↵ Select · Esc Close</span>
        <span>{isMacPlatform() ? "⌘K" : "Ctrl+K"}</span>
      </div>
    </Modal>
  );
}

export function ShortcutsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const mod = isMacPlatform() ? "⌘" : "Ctrl";
  const groups = [
    {
      title: "Navigation",
      rows: [
        { label: "Command palette & message search", keys: `${mod} K` },
        { label: "Toggle sidebar", keys: `${mod} B` },
        { label: "Back / Forward in history", keys: isMacPlatform() ? "⌘ [ / ⌘ ]" : "Alt ← / Alt →" },
        { label: "Switch space", keys: `${mod} 1–9` },
        { label: "Close detail panel / dialog", keys: "Esc" },
      ],
    },
    {
      title: "Composer & Reading",
      rows: [
        { label: "Send message (default)", keys: "Enter" },
        { label: "New line (default)", keys: "Shift Enter" },
        { label: "Mention agent or person", keys: "@" },
        { label: "Keyboard shortcuts", keys: `${mod} /` },
      ],
    },
  ];
  return (
    <Modal open={open} onClose={onClose} label="Keyboard shortcuts" title="Keyboard shortcuts" width={460}>
      <div className="px-6 pb-6 pt-3 space-y-5">
        {groups.map((g) => (
          <div key={g.title}>
            <div className="mb-2 flex items-center gap-1.5 text-[11.5px] font-semibold uppercase tracking-[0.05em] text-fg-4">
              <Keyboard className="size-3.5" />
              {g.title}
            </div>
            <div className="rounded-[7px] border border-line bg-card divide-y divide-line overflow-hidden">
              {g.rows.map((r) => (
                <div key={r.label} className="px-3.5 h-10 flex items-center justify-between gap-3 text-[13.5px]">
                  <span className="text-fg-2">{r.label}</span>
                  <kbd className="font-mono text-[11.5px] px-2 py-0.5 rounded-[4px] bg-subtle border border-line text-fg shrink-0">
                    {r.keys}
                  </kbd>
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>
    </Modal>
  );
}
