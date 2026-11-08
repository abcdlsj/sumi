import { activityLabel, type AgentPresence } from "../agentActivity";
import { buildAgentIdenticon, identityPalette } from "./agentIdenticon";

export function PixelIdentity({ name, kind = "human", seed }: { name: string; kind?: "human" | "agent"; seed: string }) {
  const initial = [...name.trim()][0]?.toLocaleUpperCase() ?? "?";
  const identicon = kind === "agent" ? buildAgentIdenticon(seed) : undefined;
  const palette = identicon ?? identityPalette(seed);
  return (
    <span
      className={`pixel-identity pixel-identity--${kind}`}
      role="img"
      aria-label={`${name} avatar`}
      title={name}
      data-agent-identicon={identicon?.signature}
      style={{ background: palette.background, color: palette.foreground }}
    >
      {identicon ? (
        <svg viewBox="0 0 8 8" aria-hidden="true" shapeRendering="crispEdges">
          {identicon.cells.map(([x, y]) => <rect key={`${x}-${y}`} x={x} y={y} width="1" height="1" fill={identicon.foreground} />)}
        </svg>
      ) : <span aria-hidden="true">{initial}</span>}
    </span>
  );
}

/** An identity that also shows whether its Agent is working or stopped on an error. */
export function PresenceIdentity({ name, kind = "human", seed, activityStatus }: { name: string; kind?: "human" | "agent"; seed: string; activityStatus?: AgentPresence }) {
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
