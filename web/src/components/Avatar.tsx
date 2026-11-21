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

function mulberry32(seed: number): () => number {
  let a = seed;
  return () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** Curated hues; the muddy and violet ranges between them are skipped. */
const HUES = [6, 22, 36, 50, 72, 96, 124, 150, 172, 190, 208, 224];

/** Cell tones: 0 empty, 1 body, 2 highlight, 3 eye, 4 accent. */
export type SpriteCell = 0 | 1 | 2 | 3 | 4;

/**
 * 8x8 mirrored sprite: a contiguous body silhouette with eyes, plus optional antenna,
 * highlight band, arms and legs. Deterministic per seed; the left half is drawn and mirrored.
 */
export function agentSprite(seed: string): { cells: SpriteCell[][]; hue: number } {
  const rnd = mulberry32(fnv(seed || "?"));
  const pick = <T,>(xs: T[]): T => xs[Math.floor(rnd() * xs.length)];
  const hue = pick(HUES);
  const g: SpriteCell[][] = Array.from({ length: 8 }, () => [0, 0, 0, 0]);
  const top = rnd() < 0.5 ? 1 : 2;
  const bottom = rnd() < 0.6 ? 6 : 5;
  const widths: number[] = [];
  let w = 2 + Math.floor(rnd() * 2) + (rnd() < 0.25 ? 1 : 0);
  for (let y = top; y <= bottom; y++) {
    if (y > top) w = Math.max(2, Math.min(4, w + pick([-1, 0, 0, 0, 1])));
    widths[y] = w;
    for (let x = 4 - w; x < 4; x++) g[y][x] = 1;
  }
  if (widths[top] === 4 && rnd() < 0.6) g[top][0] = 0;
  const ey = top + (widths[top] >= 3 && rnd() < 0.5 ? 0 : 1);
  const we = widths[ey];
  g[ey][we >= 4 ? pick([1, 2, 2]) : we === 3 ? 2 : 3] = 3;
  const by = Math.min(bottom, ey + 2);
  if (rnd() < 0.55) {
    for (let x = 4 - widths[by] + 1; x < 4; x++) if (g[by][x] === 1) g[by][x] = 2;
  } else if (rnd() < 0.5) g[by][3] = 2;
  if (top === 2) {
    if (rnd() < 0.7) {
      const ax = pick([2, 3]);
      g[0][ax] = 4;
      g[1][ax] = 1;
    } else g[1][3] = 4;
  }
  if (rnd() < 0.4) {
    const ay = Math.min(bottom, ey + 1 + Math.floor(rnd() * 2));
    const x = 4 - widths[ay] - 1;
    if (x >= 0) g[ay][x] = 1;
  }
  if (bottom < 7) {
    const lw = widths[bottom];
    const lx = pick([4 - lw, 4 - lw + 1].filter((x) => x < 3));
    if (lx !== undefined && rnd() < 0.75) g[bottom + 1][lx] = 1;
  }
  return { cells: g.map((row) => [...row, ...[...row].reverse()]), hue };
}

const SPRITE_FILLS: Record<SpriteCell, string> = {
  0: "transparent",
  1: "var(--sprite-body)",
  2: "var(--sprite-hi)",
  3: "var(--sprite-eye)",
  4: "var(--sprite-accent)",
};

const SIZES = { xs: 20, sm: 24, md: 32, lg: 40, xl: 56 } as const;
export type AvatarSize = keyof typeof SIZES;

function avatarRadius(px: number): number {
  return Math.max(4, Math.round(px * 0.18));
}

export function PixelAvatar({ seed, size = "md", label }: { seed: string; size?: AvatarSize; label?: string }) {
  const px = SIZES[size];
  const { cells, hue } = useMemo(() => agentSprite(seed), [seed]);
  // Yellows and greens read brighter at equal lightness, so darken them in light mode.
  const l = hue >= 30 && hue <= 110 ? "42%" : "52%";
  return (
    <svg
      width={px}
      height={px}
      viewBox="0 0 10 10"
      role="img"
      aria-label={label}
      className="agent-sprite shrink-0 block"
      style={{
        borderRadius: avatarRadius(px),
        ["--sprite-h" as string]: hue,
        ["--sprite-l" as string]: l,
      }}
      shapeRendering="crispEdges"
    >
      {cells.flatMap((row, y) =>
        row.map((v, x) => (v ? <rect key={`${x}-${y}`} x={1 + x} y={1 + y} width={1.02} height={1.02} fill={SPRITE_FILLS[v]} /> : null)),
      )}
    </svg>
  );
}

function initial(name: string): string {
  const first = Array.from(name.trim())[0];
  return first ? first.toUpperCase() : "?";
}

/** One letter on a rounded-square tile; the tone is seeded by the member id, not the name. */
export function HumanAvatar({ seed, name, size = "md" }: { seed: string; name: string; size?: AvatarSize }) {
  const px = SIZES[size];
  const tone = (fnv(seed || name) % 8) + 1;
  return (
    <span
      role="img"
      aria-label={name}
      className={`human-avatar avatar-tone-${tone}`}
      style={{
        width: px,
        height: px,
        borderRadius: avatarRadius(px),
        fontSize: Math.max(9, Math.round(px * 0.45)),
      }}
    >
      {initial(name)}
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

/** Square pixel status indicator with a fine hairline border; `ring` cuts it out against the avatar surface. */
export function StatusDot({ state, ring = "var(--panel)", size = 8 }: { state?: RuntimeState; ring?: string; size?: number }) {
  const s = state ?? "offline";
  const fill =
    s === "working"
      ? "var(--busy)"
      : s === "error"
        ? "var(--danger)"
        : s === "idle"
          ? "var(--online)"
          : ring === "transparent"
            ? "var(--subtle)"
            : ring;
  return (
    <span
      title={stateLabel(s)}
      className={`block shrink-0 box-border ${s === "working" ? "animate-breathe" : ""}`}
      style={{
        width: size,
        height: size,
        borderRadius: size <= 6 ? 1.5 : 2,
        background: fill,
        border: `1px solid ${s === "offline" ? "var(--text-4)" : "var(--dot-border)"}`,
        boxShadow: ring === "transparent" ? undefined : `0 0 0 1.5px ${ring}`,
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
  const dot = size === "xs" ? 6 : size === "sm" ? 7 : size === "md" ? 8 : size === "lg" ? 9 : 10;
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
