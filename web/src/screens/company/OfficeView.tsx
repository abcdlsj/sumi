import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ListTodo, MessageCircle, Users } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import {
  listAgents,
  listChannelMembers,
  listMemberDirectMessages,
  listMessages,
  type Agent,
  type Channel,
  type DirectMessage,
  type Member,
} from "../../api/client";
import { activityLabel } from "../../agentActivity";
import { PixelAgent, type AgentPose } from "../../components/company/PixelAgent";
import { PixelIdentity } from "../../components/PixelIdentity";
import { useSpaceEvents } from "../../hooks/useSpaceEvents";
import { buildGrid, isMoving, routeWalker, samePoint, spawnWalker, stepWalker, type Point, type Rect, type Walker } from "./officeSim";
import "./office.css";

const HOT_WINDOW_MS = 120_000;
const HOT_THRESHOLD = 5;
const VISIT_HOLD_MS = 45_000;

const WANDER_DELAY_MS = 25_000;
const WANDER_HOLD_MS = 6_000;
const LEISURE_HOLD_MS = 12_000;
const THINK_EVERY_MS = 250;

interface OfficeProp {
  src: string;
  x: number;
  y: number;
  width: number;
  height: number;
}

interface OfficeLayout {
  width: number;
  height: number;
  workstations: readonly { x: number; y: number }[];
  desks: readonly { x: number; y: number }[];
  wallTiles: readonly { x: number }[];
  props: readonly OfficeProp[];
  wanderSpots: readonly { x: number; y: number }[];
  leisureSpots: readonly { x: number; y: number }[];
  meetingSpots: readonly { x: number; y: number }[];
  /** Furniture and walls the walkers route around. */
  obstacles: readonly Rect[];
}

function buildOfficeLayout(count: number): OfficeLayout {
  const tier = count <= 0 ? 0 : count <= 3 ? 1 : count <= 6 ? 2 : count <= 9 ? 3 : 4;
  const extended = count > 12;
  const sizes = [
    { width: 320, height: 180 },
    { width: 480, height: 270 },
    { width: 640, height: 360 },
    { width: 800, height: 450 },
    { width: 960, height: 540 },
  ];
  // Preserve the documented 10–12 Agent canvas, then grow the room vertically
  // in four-column rows so every additional Agent receives its own station.
  const width = sizes[tier].width;
  const height = extended
    ? Math.max(sizes[tier].height, 160 + Math.ceil(count / 4) * 120)
    : sizes[tier].height;
  const wallTiles = Array.from({ length: Math.ceil(width / 64) }, (_, index) => ({
    x: Math.min(index * 64, width - 64),
  }));

  const workstations: { x: number; y: number }[] = [];
  if (tier === 1) {
    const n = Math.min(count, 3);
    const spacing = 80;
    const startX = (width - (n - 1) * spacing) / 2;
    const y = Math.round(height * 0.48);
    for (let index = 0; index < n; index += 1) {
      workstations.push({ x: Math.round(startX + index * spacing), y });
    }
  } else if (tier === 2) {
    const rows = 2;
    const cols = 3;
    const startX = (width - (cols - 1) * 80) / 2;
    const startY = Math.round(height * 0.38);
    for (let row = 0; row < rows && workstations.length < count; row += 1) {
      for (let col = 0; col < cols && workstations.length < count; col += 1) {
        workstations.push({ x: Math.round(startX + col * 80), y: startY + row * 90 });
      }
    }
  } else if (tier === 3) {
    const rows = 3;
    const cols = 3;
    const startX = (width - (cols - 1) * 80) / 2;
    const startY = Math.round(height * 0.3);
    for (let row = 0; row < rows && workstations.length < count; row += 1) {
      for (let col = 0; col < cols && workstations.length < count; col += 1) {
        workstations.push({ x: Math.round(startX + col * 80), y: startY + row * 80 });
      }
    }
  } else if (tier === 4) {
    const rows = extended ? Math.ceil(count / 4) : 3;
    const cols = 4;
    const spacingX = 112;
    const startX = (width - (cols - 1) * spacingX) / 2;
    const startY = 160;
    for (let row = 0; row < rows && workstations.length < count; row += 1) {
      for (let col = 0; col < cols && workstations.length < count; col += 1) {
        workstations.push({ x: Math.round(startX + col * spacingX), y: startY + row * 120 });
      }
    }
  }

  const desks = workstations.map((station) => ({ x: station.x, y: station.y + 24 }));
  const props: OfficeProp[] = [];
  if (tier >= 1) {
    props.push({ src: "plant.png", x: 16, y: height - 48, width: 32, height: 32 });
    props.push({ src: "plant.png", x: width - 48, y: height - 48, width: 32, height: 32 });
    props.push({ src: "water-cooler.png", x: 16, y: height - 96, width: 16, height: 32 });
    props.push({ src: "trash.png", x: width - 32, y: height - 96, width: 16, height: 16 });
    const gameX = (width - 64) / 2;
    const gameY = height - 88;
    props.push({ src: "stamping-table.png", x: gameX, y: gameY, width: 64, height: 32 });
    props.push({ src: "chair.png", x: gameX + 24, y: gameY - 20, width: 16, height: 16 });
    props.push({ src: "chair.png", x: gameX + 24, y: gameY + 40, width: 16, height: 16 });
  }
  if (tier >= 2) {
    props.push({ src: "printer.png", x: 16, y: height - 160, width: 64, height: 32 });
    props.push({ src: "coffee-maker.png", x: width - 80, y: height - 160, width: 64, height: 64 });
  }
  if (tier >= 3) {
    props.push({ src: "sink.png", x: width - 80, y: height - 240, width: 64, height: 64 });
    props.push({ src: "cabinet.png", x: width - 80, y: 16, width: 64, height: 64 });
  }
  if (tier >= 4) {
    const tableX = (width - 64) / 2;
    const tableY = (height - 64) / 2;
    props.push({ src: "writing-table.png", x: tableX, y: tableY, width: 64, height: 64 });
    props.push({ src: "chair.png", x: tableX + 24, y: tableY - 20, width: 16, height: 16 });
    props.push({ src: "chair.png", x: tableX + 24, y: tableY + 68, width: 16, height: 16 });
    props.push({ src: "chair.png", x: tableX - 20, y: tableY + 24, width: 16, height: 16 });
    props.push({ src: "chair.png", x: tableX + 68, y: tableY + 24, width: 16, height: 16 });
  }

  const leisureSpots =
    tier >= 1
      ? [
          { x: 40, y: height - 72 },
          { x: width - 48, y: height - 96 },
          { x: (width - 64) / 2 + 32, y: height - 88 + 16 },
          { x: (width - 64) / 2 + 32, y: height - 88 + 48 },
        ]
      : [];

  const blocked = [
    ...workstations.map((station) => ({
      x0: station.x - 40,
      y0: station.y - 32,
      x1: station.x + 40,
      y1: station.y + 40,
    })),
    ...props.map((prop) => ({
      x0: prop.x - 8,
      y0: prop.y - 8,
      x1: prop.x + prop.width + 8,
      y1: prop.y + prop.height + 8,
    })),
  ];
  const wanderSpots: { x: number; y: number }[] = [];
  for (let y = 104; y < height - 16; y += 48) {
    for (let x = 40; x < width - 16; x += 64) {
      if (!blocked.some((box) => x >= box.x0 && x <= box.x1 && y >= box.y0 && y <= box.y1)) {
        wanderSpots.push({ x, y });
      }
    }
  }
  if (!wanderSpots.length) wanderSpots.push({ x: width / 2, y: height - 40 });

  const meetingSpots =
    tier >= 4
      ? [
          { x: (width - 64) / 2 + 32, y: (height - 64) / 2 + 32 },
          { x: (width - 64) / 2 + 8, y: (height - 64) / 2 + 56 },
          { x: (width - 64) / 2 + 56, y: (height - 64) / 2 + 56 },
          { x: (width - 64) / 2 + 32, y: (height - 64) / 2 + 80 },
        ]
      : tier === 3
        ? [{ x: width / 2, y: 175 }]
        : tier === 2
          ? [{ x: width / 2, y: 182 }]
          : [{ x: width / 2, y: height - 60 }];

  const obstacles: Rect[] = [
    { x0: 0, y0: 0, x1: width, y1: 70 },
    ...workstations.map((station) => ({ x0: station.x - 34, y0: station.y - 34, x1: station.x + 34, y1: station.y + 14 })),
    ...props
      .filter((prop) => prop.src !== "chair.png")
      .map((prop) => ({ x0: prop.x - 2, y0: prop.y - 2, x1: prop.x + prop.width + 2, y1: prop.y + prop.height + 2 })),
  ];

  return {
    width,
    height,
    workstations,
    desks,
    wallTiles,
    props,
    wanderSpots,
    leisureSpots,
    // A single gathering point becomes a loose ring so a group never stacks.
    meetingSpots: meetingSpots.length > 1 ? meetingSpots : ringAround(meetingSpots[0], 6),
    obstacles,
  };
}

interface DmVisit {
  visitorId: string;
  hostId: string;
  until: number;
}

interface HotGroup {
  channelId: string;
  channelSlug: string;
  memberIds: string[];
  until: number;
}

export function CompanyOfficeView({
  spaceSlug,
  spaceId,
  channels,
  directMessages,
  members,
  currentMember,
}: {
  spaceSlug: string;
  spaceId: string;
  channels: Channel[];
  directMessages: DirectMessage[];
  members: Member[];
  currentMember: Member;
}) {
  const reduced = useReducedMotion();
  const agents = useQuery({
    queryKey: ["agents", spaceId],
    queryFn: () => listAgents(spaceId),
    enabled: true,
  });
  const activeAgents = useMemo(
    () =>
      (agents.data ?? [])
        .filter((agent) => agent.desired_lifecycle === "active")
        .sort(
          (left, right) =>
            left.created_at.localeCompare(right.created_at) ||
            left.member_id.localeCompare(right.member_id),
        ),
    [agents.data],
  );
  const layout = useMemo(() => buildOfficeLayout(activeAgents.length), [activeAgents.length]);
  const [dmVisits, setDmVisits] = useState<ReadonlyMap<string, DmVisit>>(new Map());
  const [hotGroups, setHotGroups] = useState<ReadonlyMap<string, HotGroup>>(new Map());
  const [governorDms, setGovernorDms] = useState<ReadonlyMap<string, { a: string; b: string }>>(
    () => new Map(),
  );
  const channelActivity = useRef<Map<string, number[]>>(new Map());
  const stageRef = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(1);

  const isGovernor = ["owner", "admin"].includes(currentMember.access_level);
  const viewerDmDirectory = useMemo(() => {
    const directory = new Map<string, { a: string; b: string }>();
    for (const dm of directMessages) {
      directory.set(dm.channel_id, { a: currentMember.id, b: dm.other_member.id });
    }
    return directory;
  }, [currentMember.id, directMessages]);
  useEffect(() => {
    if (!isGovernor || activeAgents.length === 0) return;
    let cancelled = false;
    void Promise.all(activeAgents.map((agent) => listMemberDirectMessages(agent.member_id)))
      .then((results) => {
        if (cancelled) return;
        const directory = new Map<string, { a: string; b: string }>();
        for (let index = 0; index < activeAgents.length; index += 1) {
          const subject = activeAgents[index].member_id;
          for (const dm of results[index] ?? []) {
            if (dm.other_member.kind !== "agent") continue;
            directory.set(dm.channel_id, { a: subject, b: dm.other_member.id });
          }
        }
        setGovernorDms(directory);
      })
      .catch(() => {
        if (!cancelled) setGovernorDms(new Map());
      });
    return () => {
      cancelled = true;
    };
  }, [activeAgents, isGovernor]);
  const dmDirectory = useMemo(() => {
    const merged = new Map(viewerDmDirectory);
    for (const [channelId, pair] of governorDms) merged.set(channelId, pair);
    return merged;
  }, [governorDms, viewerDmDirectory]);
  async function handleDmMessage(pair: { a: string; b: string }, channelId: string) {
    let authorId: string | undefined;
    try {
      const page = await listMessages(channelId);
      authorId = page.messages[page.messages.length - 1]?.author.id;
    } catch {
      // Directory still lets us visualise the discussion without the author.
    }
    const visitor =
      typeof authorId === "string" && (authorId === pair.a || authorId === pair.b)
        ? authorId
        : pair.a;
    const agentIds = new Set(activeAgents.map((agent) => agent.member_id));
    const agentVisitor = agentIds.has(visitor) ? visitor : (visitor === pair.a ? pair.b : pair.a);
    const host = agentVisitor === pair.a ? pair.b : pair.a;
    const now = Date.now();
    setDmVisits((current) =>
      new Map(current).set(channelId, {
        visitorId: agentVisitor,
        hostId: host,
        until: now + VISIT_HOLD_MS,
      }),
    );
  }

  function recordChannelMessage(channelId: string, now: number) {
    const cutoff = now - HOT_WINDOW_MS;
    const times = [...(channelActivity.current.get(channelId) ?? []), now].filter(
      (timestamp) => timestamp >= cutoff,
    );
    channelActivity.current.set(channelId, times);
    if (times.length >= HOT_THRESHOLD) void markChannelHot(channelId, now);
  }

  async function markChannelHot(channelId: string, now: number) {
    const slug = channels.find((channel) => channel.id === channelId)?.slug ?? channelId;
    setHotGroups((current) => {
      const existing = current.get(channelId);
      if (!existing) return current;
      return new Map(current).set(channelId, { ...existing, until: now + HOT_WINDOW_MS });
    });
    if (hotGroups.has(channelId)) return;
    try {
      const channelMembers = await listChannelMembers(channelId);
      const memberIds = channelMembers.members
        .filter((member) => member.kind === "agent")
        .map((member) => member.id);
      setHotGroups((current) =>
        current.has(channelId)
          ? new Map(current).set(channelId, {
              ...(current.get(channelId) as HotGroup),
              until: now + HOT_WINDOW_MS,
            })
          : new Map(current).set(channelId, {
              channelId,
              channelSlug: slug,
              memberIds,
              until: now + HOT_WINDOW_MS,
            }),
      );
    } catch {
      // A channel that became unreadable between event and fetch is ignored.
    }
  }

  useSpaceEvents(spaceId, ({ channelId }) => {
    const now = Date.now();
    const pair = dmDirectory.get(channelId);
    if (pair) {
      void handleDmMessage(pair, channelId);
      return;
    }
    recordChannelMessage(channelId, now);
  });

  useEffect(() => {
    const timer = window.setInterval(() => {
      const now = Date.now();
      setDmVisits((current) => pruneByUntil(current, now));
      setHotGroups((current) => pruneByUntil(current, now));
      const cutoff = now - HOT_WINDOW_MS;
      for (const [channelId, times] of channelActivity.current) {
        const kept = times.filter((timestamp) => timestamp >= cutoff);
        if (kept.length) channelActivity.current.set(channelId, kept);
        else channelActivity.current.delete(channelId);
      }
    }, 2000);
    return () => window.clearInterval(timer);
  }, []);

  const deskByMember = useMemo(
    () => new Map(activeAgents.map((agent, index) => [agent.member_id, layout.desks[index]])),
    [activeAgents, layout.desks],
  );
  const scene = useOfficeScene({ activeAgents, layout, deskByMember, dmVisits, hotGroups, reduced });

  useEffect(() => {
    const element = stageRef.current;
    if (!element) return;
    const update = () => setScale(element.clientWidth / layout.width);
    update();
    const observer = typeof ResizeObserver === "undefined" ? undefined : new ResizeObserver(update);
    observer?.observe(element);
    window.addEventListener("resize", update);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", update);
    };
  }, [layout.width]);

  const memberById = useMemo(
    () => new Map(members.map((member) => [member.id, member])),
    [members],
  );
  const counts = useMemo(() => officeCounts(agents.data ?? []), [agents.data]);
  const [focusedId, setFocusedId] = useState<string>();

  return (
    <section className="office-workspace" aria-labelledby="office-heading">
      <header className="office-header">
        <div className="page-title">
          <h1 id="office-heading" tabIndex={-1}>Office</h1>
          <p>Live view of every Agent at work.</p>
        </div>
        <Link className="command-button office-tasks-link" to="/s/$spaceSlug/tasks" params={{ spaceSlug }}>
          <ListTodo aria-hidden="true" />Tasks
        </Link>
      </header>

      <div className="office-body">
      <div className="office-stage-column">
      <div
        className="office-stage-wrap"
        ref={stageRef}
        style={{ aspectRatio: `${layout.width} / ${layout.height}` }}
      >
        {activeAgents.length === 0 ? (
          <div className="office-empty">
            <Users aria-hidden="true" />
            <h2>No Agents are paired yet</h2>
            <p>Pair a Computer and create an Agent to see the office come alive.</p>
            <Link className="command-button command-button--accent" to="/s/$spaceSlug/members" params={{ spaceSlug }}>
              Open Members
            </Link>
          </div>
        ) : (
          <div
            className="office-room"
            style={{ width: layout.width, height: layout.height, transform: `scale(${scale})` }}
          >
            <OfficeFurniture activeAgents={activeAgents} layout={layout} />
            {activeAgents.map((agent) => {
              const actor = scene.get(agent.member_id);
              if (!actor) return null;
              const status = agent.activity_status;
              const member = memberById.get(agent.member_id);
              const name = member?.display_name ?? agent.name;
              const offline = agent.computer_reachable === false;
              return (
                <Link
                  key={agent.member_id}
                  className={`office-agent${offline ? " office-agent--offline" : ""}${focusedId === agent.member_id ? " office-agent--focused" : ""}`}
                  onMouseEnter={() => setFocusedId(agent.member_id)}
                  onMouseLeave={() => setFocusedId(undefined)}
                  onFocus={() => setFocusedId(agent.member_id)}
                  onBlur={() => setFocusedId(undefined)}
                  to="/s/$spaceSlug/agents/$agentId"
                  params={{ spaceSlug, agentId: agent.member_id }}
                  style={{
                    transform: `translate(${Math.round(actor.x - 16)}px, ${Math.round(actor.y - 32)}px)`,
                    zIndex: depth(actor.y),
                  }}
                  title={`${name} · ${activityLabel(status)}`}
                >
                  <PixelAgent
                    variant={agentVariant(agent.member_id)}
                    pose={actor.pose}
                    talking={actor.talking}
                    flip={actor.facing < 0}
                  />
                  <span className="office-agent-label">
                    <i className={`office-status-dot office-status-dot--${status}`} aria-hidden="true" />
                    <span>{name}</span>
                  </span>
                </Link>
              );
            })}
          </div>
        )}
      </div>
      </div>
      <aside className="office-roster" aria-label="Agents in the office">
        <header className="office-roster-heading">
          <h2>Agents</h2>
          <span>{rosterSummary(counts)}</span>
        </header>
        {activeAgents.length === 0 ? (
          <p className="office-roster-empty">No Agents yet.</p>
        ) : (
          <ul className="office-roster-list">
            {activeAgents.map((agent) => {
              const name = memberById.get(agent.member_id)?.display_name ?? agent.name;
              const status = agent.activity_status;
              const offline = agent.computer_reachable === false;
              const live = status === "working" || status === "dispatched";
              const detail = offline
                ? "Computer offline"
                : live && agent.activity?.label
                  ? agent.activity.label
                  : activityLabel(status);
              return (
                <li key={agent.member_id}>
                  <Link
                    className={`office-roster-row${focusedId === agent.member_id ? " office-roster-row--focused" : ""}`}
                    to="/s/$spaceSlug/agents/$agentId"
                    params={{ spaceSlug, agentId: agent.member_id }}
                    onMouseEnter={() => setFocusedId(agent.member_id)}
                    onMouseLeave={() => setFocusedId(undefined)}
                    onFocus={() => setFocusedId(agent.member_id)}
                    onBlur={() => setFocusedId(undefined)}
                  >
                    <span className={`presence-identity presence-identity--${status}`}>
                      <PixelIdentity name={name} kind="agent" seed={agent.member_id} />
                    </span>
                    <span className="office-roster-copy">
                      <strong>{name}</strong>
                      <small className={live && !offline ? "office-roster-live" : undefined}>{detail}</small>
                    </span>
                  </Link>
                </li>
              );
            })}
          </ul>
        )}
        {hotGroups.size ? (
          <section className="office-gatherings" aria-label="Active group conversations">
            <h3>Gathering now</h3>
            {[...hotGroups.values()].map((group) => (
              <Link
                key={group.channelId}
                className="office-hot-chip"
                to="/s/$spaceSlug/channels/$channelSlug"
                params={{ spaceSlug, channelSlug: group.channelSlug }}
                title={`#${group.channelSlug} is busy`}
              >
                <MessageCircle aria-hidden="true" />#{group.channelSlug}
              </Link>
            ))}
          </section>
        ) : null}
        <footer className="office-roster-footer">
          {isGovernor ? <p>Agent-to-Agent DMs are visible because you govern this Space.</p> : null}
          <p>
            Scene &amp; sprites by{" "}
            <a href="https://arlantr.itch.io/free-office-pixel-art" target="_blank" rel="noreferrer">
              Arlan_TR
            </a>
          </p>
        </footer>
      </aside>
      </div>
    </section>
  );
}

function OfficeFurniture({ activeAgents, layout }: { activeAgents: Agent[]; layout: OfficeLayout }) {
  return (
    <>
      {layout.wallTiles.map((tile, index) => (
        <OfficeSprite
          key={`wall-${index}`}
          src="partition-wall.png"
          x={tile.x}
          y={0}
          width={64}
          height={64}
        />
      ))}
      {layout.props.map((prop, index) => (
        <OfficeSprite key={`${prop.src}-${index}`} {...prop} />
      ))}
      {layout.workstations.map((station, index) => {
        const working = activeAgents[index]?.activity_status === "working";
        return (
          <span
            key={`${station.x}-${station.y}`}
            className="office-workstation"
            aria-hidden="true"
            style={{ transform: `translate(${station.x - 64}px, ${station.y - 64}px)`, zIndex: depth(station.y + 14) }}
          >
            <OfficeSprite src="desk.png" x={32} y={48} width={64} height={32} />
            <OfficeSprite
              src={working ? "pc-on.png" : "pc-off.png"}
              x={48}
              y={32}
              width={32}
              height={32}
            />
            <OfficeSprite src="chair.png" x={56} y={76} width={16} height={16} />
          </span>
        );
      })}
    </>
  );
}

function OfficeSprite({
  src,
  x,
  y,
  width,
  height,
}: {
  src: string;
  x: number;
  y: number;
  width: number;
  height: number;
}) {
  return (
    <span
      className="office-sprite"
      style={{
        width,
        height,
        transform: `translate(${x}px, ${y}px)`,
        zIndex: depth(y + height),
        backgroundImage: `url("/office/${src}")`,
      }}
      aria-hidden="true"
    />
  );
}

function ringAround(centre: Point, count: number): Point[] {
  return Array.from({ length: count }, (_, index) => {
    const angle = (index / count) * Math.PI * 2 - Math.PI / 2;
    return { x: Math.round(centre.x + Math.cos(angle) * 28), y: Math.round(centre.y + Math.sin(angle) * 16) };
  });
}

/** Stack order follows the feet: whoever stands lower in the room is in front. */
function depth(y: number): number {
  return 10 + Math.round(y);
}

function agentVariant(seed: string): number {
  let hash = 0;
  for (let index = 0; index < seed.length; index += 1) {
    hash = (hash * 31 + seed.charCodeAt(index)) >>> 0;
  }
  return hash % 4;
}

function pruneByUntil<T extends { until: number }>(current: ReadonlyMap<string, T>, now: number): ReadonlyMap<string, T> {
  let changed = false;
  const next = new Map(current);
  for (const [key, value] of next) {
    if (value.until <= now) {
      next.delete(key);
      changed = true;
    }
  }
  return changed ? next : current;
}

function rosterSummary(counts: Array<{ status: string; count: number }>): string {
  const total = counts.reduce((sum, group) => sum + group.count, 0);
  const busy = counts
    .filter((group) => group.status === "working" || group.status === "dispatched")
    .reduce((sum, group) => sum + group.count, 0);
  return busy ? `${total} · ${busy} working` : String(total);
}

function officeCounts(agents: Agent[]): Array<{ status: string; label: string; count: number }> {
  const counts = new Map<string, number>();
  for (const agent of agents) {
    if (agent.desired_lifecycle !== "active") continue;
    const status = agent.activity_status;
    counts.set(status, (counts.get(status) ?? 0) + 1);
  }
  return [...counts.entries()].map(([status, count]) => ({
    status,
    label: activityLabel(status as Agent["activity_status"]),
    count,
  }));
}

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false,
  );
  useEffect(() => {
    const query = window.matchMedia?.("(prefers-reduced-motion: reduce)");
    if (!query) return;
    const onChange = () => setReduced(query.matches);
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);
  return reduced;
}

interface Actor {
  x: number;
  y: number;
  facing: -1 | 1;
  pose: AgentPose;
  talking: boolean;
}

type MindKind = "desk" | "visit" | "meet" | "leisure" | "wander";

interface Mind {
  kind: MindKind;
  target: Point;
  /** Where to face once there (the host's desk, the meeting centre). */
  faceToward?: Point;
  arrivedAt?: number;
  hold: number;
  nextIdleAt: number;
}

interface SceneInputs {
  activeAgents: Agent[];
  layout: OfficeLayout;
  deskByMember: Map<string, Point>;
  dmVisits: ReadonlyMap<string, DmVisit>;
  hotGroups: ReadonlyMap<string, HotGroup>;
}

/**
 * Where each Agent stands and what it is doing. Work, DM visits and channel
 * gatherings decide a destination; an idle Agent sits for a while, then takes
 * a short stroll or coffee break and comes back. Walkers route around the
 * furniture at a steady pace. Under reduced motion everyone is shown at their
 * destination and idle Agents stay at their desks.
 */
function useOfficeScene(inputs: SceneInputs & { reduced: boolean }): Map<string, Actor> {
  const { activeAgents, layout, deskByMember, dmVisits, hotGroups, reduced } = inputs;
  const grid = useMemo(() => buildGrid(layout.width, layout.height, layout.obstacles), [layout]);
  const inputsRef = useRef<SceneInputs>({ activeAgents, layout, deskByMember, dmVisits, hotGroups });
  useEffect(() => {
    inputsRef.current = { activeAgents, layout, deskByMember, dmVisits, hotGroups };
  }, [activeAgents, layout, deskByMember, dmVisits, hotGroups]);
  const [animated, setAnimated] = useState<{ layout: OfficeLayout; actors: Map<string, Actor> }>();

  useEffect(() => {
    if (reduced) return;
    const walkers = new Map<string, Walker>();
    const minds = new Map<string, Mind>();
    let frame = 0;
    let last = performance.now();
    let lastThink = -Infinity;
    let wasMoving = true;

    const loop = (time: number) => {
      const seconds = Math.min(0.1, (time - last) / 1000);
      last = time;
      const now = Date.now();
      const current = inputsRef.current;
      let changed = false;
      if (time - lastThink >= THINK_EVERY_MS) {
        lastThink = time;
        changed = think(current, grid, walkers, minds, now);
      }
      let moving = false;
      for (const [id, walker] of walkers) {
        if (!isMoving(walker)) continue;
        const next = stepWalker(walker, seconds);
        walkers.set(id, next);
        moving = true;
        if (!isMoving(next)) {
          const mind = minds.get(id);
          if (mind) minds.set(id, { ...mind, arrivedAt: now });
        }
      }
      if (changed || moving || wasMoving) {
        setAnimated({ layout: current.layout, actors: snapshot(current, walkers, minds) });
      }
      wasMoving = moving;
      frame = window.requestAnimationFrame(loop);
    };
    frame = window.requestAnimationFrame(loop);
    return () => window.cancelAnimationFrame(frame);
  }, [grid, reduced]);

  if (reduced || !animated || animated.layout !== layout) {
    return staticScene({ activeAgents, layout, deskByMember, dmVisits, hotGroups });
  }
  return animated.actors;
}

/** Decide every Agent's destination; returns whether anything changed. */
function think(
  inputs: SceneInputs,
  grid: ReturnType<typeof buildGrid>,
  walkers: Map<string, Walker>,
  minds: Map<string, Mind>,
  now: number,
): boolean {
  const { activeAgents, layout, deskByMember } = inputs;
  let changed = false;
  const ids = new Set(activeAgents.map((agent) => agent.member_id));
  for (const id of [...walkers.keys()]) {
    if (!ids.has(id)) {
      walkers.delete(id);
      minds.delete(id);
      changed = true;
    }
  }
  const reserved = (self: string) =>
    [...minds.entries()].filter(([id]) => id !== self).map(([, mind]) => mind.target);

  for (const agent of activeAgents) {
    const id = agent.member_id;
    const desk = deskByMember.get(id) ?? layout.desks[0];
    if (!desk) continue;
    let walker = walkers.get(id);
    if (!walker) {
      walker = spawnWalker(desk);
      changed = true;
    }
    const previous = minds.get(id) ?? {
      kind: "desk" as const,
      target: desk,
      arrivedAt: now,
      hold: 0,
      nextIdleAt: now + WANDER_DELAY_MS * (0.4 + Math.random() * 0.8),
    };
    const arrived = previous.arrivedAt !== undefined && !isMoving(walker);
    const forced = forcedMind(agent, inputs, reserved(id));
    let mind: Mind = previous;
    if (forced) {
      mind = { ...forced, hold: 0, nextIdleAt: now + WANDER_DELAY_MS, arrivedAt: undefined };
    } else if (previous.kind === "visit" || previous.kind === "meet") {
      mind = backToDesk(desk, now);
    } else if (previous.kind === "desk" && !samePoint(previous.target, desk)) {
      mind = backToDesk(desk, now);
    } else if (previous.kind === "desk" && arrived && now >= previous.nextIdleAt) {
      mind = idleOuting(layout, desk, reserved(id), now);
    } else if ((previous.kind === "leisure" || previous.kind === "wander") && arrived && now - (previous.arrivedAt ?? now) >= previous.hold) {
      mind = Math.random() < 0.35 ? idleOuting(layout, desk, [...reserved(id), previous.target], now) : backToDesk(desk, now);
    }
    if (!samePoint(mind.target, previous.target) || mind.kind !== previous.kind) {
      if (samePoint(mind.target, previous.target)) mind = { ...mind, arrivedAt: previous.arrivedAt };
      changed = true;
    }
    const routed = routeWalker(grid, walker, mind.target);
    if (routed !== walker && !isMoving(routed) && mind.arrivedAt === undefined) mind = { ...mind, arrivedAt: now };
    walkers.set(id, routed);
    minds.set(id, mind);
  }
  return changed;
}

function forcedMind(agent: Agent, inputs: SceneInputs, reserved: Point[]): Omit<Mind, "hold" | "nextIdleAt"> | undefined {
  const { deskByMember, dmVisits, hotGroups, layout } = inputs;
  const id = agent.member_id;
  const desk = deskByMember.get(id) ?? layout.desks[0];
  if (agent.computer_reachable === false || agent.activity_status === "working" || agent.activity_status === "dispatched") {
    return { kind: "desk", target: desk };
  }
  const visit = [...dmVisits.values()].find((candidate) => candidate.visitorId === id);
  const hostDesk = visit ? deskByMember.get(visit.hostId) : undefined;
  if (visit && hostDesk) {
    const sides = [{ x: hostDesk.x + 34, y: hostDesk.y - 4 }, { x: hostDesk.x - 34, y: hostDesk.y - 4 }];
    const target = sides.find((side) => !reserved.some((point) => samePoint(point, side))) ?? sides[0];
    return { kind: "visit", target, faceToward: hostDesk };
  }
  if (visit && !hostDesk) return undefined;
  const group = [...hotGroups.values()].find((candidate) => candidate.memberIds.includes(id));
  if (group && layout.meetingSpots.length) {
    const index = group.memberIds.filter((memberId) => deskByMember.has(memberId)).indexOf(id);
    const spot = layout.meetingSpots[Math.max(0, index) % layout.meetingSpots.length];
    const centre = averagePoint(layout.meetingSpots);
    return { kind: "meet", target: spot, faceToward: centre };
  }
  return undefined;
}

function backToDesk(desk: Point, now: number): Mind {
  return { kind: "desk", target: desk, hold: 0, nextIdleAt: now + 20_000 + Math.random() * 25_000 };
}

function idleOuting(layout: OfficeLayout, desk: Point, reserved: Point[], now: number): Mind {
  const free = (spots: readonly Point[]) => spots.filter((spot) => !reserved.some((point) => samePoint(point, spot)));
  const leisure = free(layout.leisureSpots);
  const wander = free(layout.wanderSpots);
  const useLeisure = leisure.length > 0 && (Math.random() < 0.45 || wander.length === 0);
  const pool = useLeisure ? leisure : wander;
  if (!pool.length) return backToDesk(desk, now);
  const target = pool[Math.floor(Math.random() * pool.length)];
  return {
    kind: useLeisure ? "leisure" : "wander",
    target,
    hold: (useLeisure ? LEISURE_HOLD_MS : WANDER_HOLD_MS) * (0.7 + Math.random() * 0.6),
    nextIdleAt: now,
  };
}

function snapshot(inputs: SceneInputs, walkers: Map<string, Walker>, minds: Map<string, Mind>): Map<string, Actor> {
  const actors = new Map<string, Actor>();
  for (const agent of inputs.activeAgents) {
    const walker = walkers.get(agent.member_id);
    const mind = minds.get(agent.member_id);
    if (!walker || !mind) continue;
    const moving = isMoving(walker);
    actors.set(agent.member_id, actorFor(agent, mind, walker, !moving, moving, inputs, walkers, minds));
  }
  return actors;
}

function actorFor(
  agent: Agent,
  mind: Mind | Omit<Mind, "hold" | "nextIdleAt">,
  position: Point & { facing?: -1 | 1 },
  arrived: boolean,
  moving: boolean,
  inputs: SceneInputs,
  walkers?: Map<string, Walker>,
  minds?: Map<string, Mind>,
): Actor {
  const working = agent.activity_status === "working" || agent.activity_status === "dispatched";
  const pose: AgentPose = moving
    ? "walk"
    : mind.kind === "desk"
      ? working ? "typing" : "sit"
      : mind.kind === "leisure"
        ? "leisure"
        : "stand";
  let facing: -1 | 1 = position.facing ?? 1;
  if (arrived && mind.faceToward && Math.abs(mind.faceToward.x - position.x) > 2) {
    facing = mind.faceToward.x < position.x ? -1 : 1;
  }
  const hosting = [...inputs.dmVisits.values()].some((visit) => {
    if (visit.hostId !== agent.member_id) return false;
    if (!walkers || !minds) return true;
    const visitor = walkers.get(visit.visitorId);
    return Boolean(visitor && !isMoving(visitor) && minds.get(visit.visitorId)?.kind === "visit");
  });
  const talking = (arrived && (mind.kind === "visit" || mind.kind === "meet")) || hosting;
  return { x: position.x, y: position.y, facing, pose, talking };
}

/** Everyone at their destination, idle Agents at their desks. */
function staticScene(inputs: SceneInputs): Map<string, Actor> {
  const actors = new Map<string, Actor>();
  const reservedTargets: Point[] = [];
  for (const agent of inputs.activeAgents) {
    const desk = inputs.deskByMember.get(agent.member_id) ?? inputs.layout.desks[0];
    if (!desk) continue;
    const mind = forcedMind(agent, inputs, reservedTargets) ?? { kind: "desk" as const, target: desk };
    reservedTargets.push(mind.target);
    actors.set(agent.member_id, actorFor(agent, mind, mind.target, true, false, inputs));
  }
  return actors;
}

function averagePoint(points: readonly Point[]): Point {
  const total = points.reduce((sum, point) => ({ x: sum.x + point.x, y: sum.y + point.y }), { x: 0, y: 0 });
  return { x: total.x / points.length, y: total.y / points.length };
}
