import { useMemo } from "react";
import type { BootstrapData, Member, MemberKind, RuntimeState } from "../api";

function fnv(seed: string): number {
  let h = 2166136261;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

/** 5x5 horizontally mirrored grid, GitHub identicon style; hue and pattern derive from the seed. */
export function identicon(seed: string): { cells: boolean[]; hue: number } {
  const h = fnv(seed || "?");
  const h2 = fnv(`${seed}:sumi`);
  const cells: boolean[] = new Array(25).fill(false);
  let on = 0;
  for (let r = 0; r < 5; r++) {
    for (let c = 0; c < 3; c++) {
      const bit = ((c < 2 ? h : h2) >>> ((r * 3 + c) % 30)) & 1;
      if (bit) {
        cells[r * 5 + c] = true;
        cells[r * 5 + (4 - c)] = true;
        on++;
      }
    }
  }
  // Guarantee a readable glyph: never empty, never solid.
  if (on < 3) [6, 8, 12, 16, 18].forEach((i) => (cells[i] = true));
  if (on > 13) [0, 4, 20, 24].forEach((i) => (cells[i] = false));
  return { cells, hue: h2 % 360 };
}

const SIZES = { xs: 20, sm: 24, md: 32, lg: 40, xl: 56 } as const;
export type AvatarSize = keyof typeof SIZES;

export function PixelAvatar({ seed, size = "md", label }: { seed: string; size?: AvatarSize; label?: string }) {
  const px = SIZES[size];
  const { cells, hue } = useMemo(() => identicon(seed), [seed]);
  const pad = 0.6;
  return (
    <svg
      width={px}
      height={px}
      viewBox={`0 0 ${5 + pad * 2} ${5 + pad * 2}`}
      role="img"
      aria-label={label}
      className="shrink-0 block"
      style={{ borderRadius: Math.max(5, px * 0.24), background: "var(--avatar-bg)" }}
      shapeRendering="crispEdges"
    >
      {cells.map((v, i) =>
        v ? (
          <rect
            key={i}
            x={pad + (i % 5)}
            y={pad + Math.floor(i / 5)}
            width={1.02}
            height={1.02}
            fill={`hsl(${hue} 62% 52%)`}
          />
        ) : null,
      )}
    </svg>
  );
}

function initials(name: string): string {
  const parts = name.split(/[_\s.-]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return (name.slice(0, 2) || "?").toUpperCase();
}

export function HumanAvatar({ seed, name, size = "md" }: { seed: string; name: string; size?: AvatarSize }) {
  const px = SIZES[size];
  const hue = fnv(seed || name) % 360;
  return (
    <span
      aria-label={name}
      className="shrink-0 rounded-full inline-flex items-center justify-center font-semibold select-none"
      style={{
        width: px,
        height: px,
        fontSize: Math.max(10, Math.round(px * 0.38)),
        background: `hsl(${hue} 60% 92%)`,
        color: `hsl(${hue} 45% 32%)`,
      }}
    >
      {initials(name)}
    </span>
  );
}

const STATE_LABEL: Record<RuntimeState, string> = {
  idle: "Idle",
  working: "Working",
  error: "Error",
  offline: "Offline",
};

export function stateLabel(state?: RuntimeState): string {
  return STATE_LABEL[state ?? "offline"];
}

/** Status dot; `ring` must match the surface it sits on so it reads as a cut-out. */
export function StatusDot({ state, ring = "var(--panel)", size = 10 }: { state?: RuntimeState; ring?: string; size?: number }) {
  const s = state ?? "offline";
  const fill =
    s === "working" ? "var(--work)" : s === "error" ? "var(--danger)" : s === "idle" ? "var(--text-4)" : "transparent";
  return (
    <span
      title={stateLabel(s)}
      className={`block rounded-full shrink-0 ${s === "working" ? "animate-breathe" : ""}`}
      style={{
        width: size,
        height: size,
        background: fill,
        boxShadow: `0 0 0 2px ${ring}${s === "offline" ? ", inset 0 0 0 1.5px var(--text-4)" : ""}`,
      }}
    />
  );
}

export function MemberAvatar({
  id,
  name,
  kind,
  size = "md",
  state,
  ring,
}: {
  id: string;
  name: string;
  kind: MemberKind;
  size?: AvatarSize;
  state?: RuntimeState;
  ring?: string;
}) {
  const avatar = kind === "agent" ? <PixelAvatar seed={id} size={size} label={`${name} (agent)`} /> : <HumanAvatar seed={id} name={name} size={size} />;
  if (kind !== "agent" || state === undefined) return avatar;
  const dot = size === "xs" ? 7 : size === "sm" ? 8 : size === "md" ? 10 : 12;
  return (
    <span className="relative inline-flex shrink-0">
      {avatar}
      <span className="absolute -right-0.5 -bottom-0.5">
        <StatusDot state={state} ring={ring} size={dot} />
      </span>
    </span>
  );
}

export interface FocusInfo {
  label: string;
  convID?: string;
  taskID?: string;
}

/** Resolves what a working agent is attending to, using only data the viewer can already see. */
export function agentFocus(boot: BootstrapData, agent: Member): FocusInfo | null {
  const focus = agent.agent?.focus_conversation_id;
  if (!focus || agent.agent?.runtime_state !== "working") return null;
  const task = boot.tasks.find((t) => t.conversation_id === focus);
  if (task) return { label: `#${task.number} ${task.title}`, taskID: task.id };
  const conv = boot.conversations.find((c) => c.id === focus);
  if (conv?.kind === "dm") {
    return { label: conv.peer_member_id === agent.id ? "your DM" : `DM with ${conv.peer_display_name}`, convID: conv.id };
  }
  if (conv) return { label: `#${conv.slug}`, convID: conv.id };
  return { label: "a private conversation" };
}

export function agentStatusText(boot: BootstrapData, agent: Member): string {
  const st = agent.agent?.runtime_state ?? "offline";
  if (st === "working") {
    const f = agentFocus(boot, agent);
    return f ? `Working in ${f.label}` : "Working";
  }
  if (st === "error") return agent.agent?.last_error_code ? `Error · ${agent.agent.last_error_code}` : "Error";
  if (st === "offline") return "Offline";
  return "Idle";
}
