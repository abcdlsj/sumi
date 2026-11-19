import type { MemberKind, RuntimeState, TaskStatus } from "../api";

function hashBits(seed: string): number {
  let h = 2166136261;
  for (let i = 0; i < seed.length; i++) {
    h ^= seed.charCodeAt(i);
    h = Math.imul(h, 16777619);
  }
  return h >>> 0;
}

export function MemberAvatar({
  id,
  name,
  kind,
  size = "md",
}: {
  id: string;
  name: string;
  kind: MemberKind;
  size?: "sm" | "md";
}) {
  const dim = size === "sm" ? "w-4 h-4 text-[9px]" : "w-7 h-7 text-[11px]";
  if (kind === "human") {
    return (
      <span
        className={`${dim} rounded-full border border-[#D5D1C7] bg-[#EBE4D8] text-[#5B4930] font-semibold inline-flex items-center justify-center shrink-0 select-none`}
      >
        {(name[0] ?? "?").toUpperCase()}
      </span>
    );
  }

  // Deterministic 4x4 horizontally symmetric pixel stamp for Agents.
  const bits = hashBits(id || name);
  const cells: boolean[] = [];
  for (let r = 0; r < 4; r++) {
    const left0 = ((bits >> (r * 2)) & 1) === 1;
    const left1 = ((bits >> (r * 2 + 1)) & 1) === 1;
    cells.push(left0, left1, left1, left0);
  }
  return (
    <span
      className={`${size === "sm" ? "w-4 h-4 p-[2px]" : "w-7 h-7 p-[3px]"} grid grid-cols-4 gap-[1px] rounded-[4px] border border-[#D5D1C7] bg-[#F1EFEA] shrink-0`}
      aria-label={`${name} (Agent)`}
    >
      {cells.map((on, idx) => (
        <span key={idx} className={on ? "bg-[#161614] rounded-[0.5px]" : ""} />
      ))}
    </span>
  );
}

export function RuntimeDot({ state }: { state?: RuntimeState }) {
  const color =
    state === "working"
      ? "bg-[#0F8B78]"
      : state === "error"
        ? "bg-[#C84332]"
        : state === "idle"
          ? "bg-[#BDB9AE]"
          : "bg-[#D5D1C7]";
  return <span className={`w-1.5 h-1.5 rounded-full inline-block shrink-0 ${color}`} title={state ?? "offline"} />;
}

export function TaskStatusGlyph({ status }: { status: TaskStatus }) {
  switch (status) {
    case "todo":
      return <span className="w-3 h-3 rounded-full border border-dashed border-[#8A867D] inline-block shrink-0" title="To Do" />;
    case "in_progress":
      return (
        <span className="w-3 h-3 rounded-full border-[1.75px] border-[#0F8B78] inline-flex items-center justify-center shrink-0" title="In Progress">
          <span className="w-1.5 h-1.5 rounded-full bg-[#0F8B78]" />
        </span>
      );
    case "in_review":
      return (
        <span className="w-3 h-3 rounded-full border-[1.75px] border-[#A84010] inline-flex items-center justify-center shrink-0" title="In Review">
          <span className="w-1 h-1 rounded-full bg-[#A84010]" />
        </span>
      );
    case "done":
      return <span className="w-3 h-3 rounded-full bg-[#2E7D5B] text-white text-[8px] inline-flex items-center justify-center shrink-0" title="Done">✓</span>;
    case "closed":
      return <span className="w-3 h-3 rounded-full bg-[#D5D1C7] text-[#6E6B64] text-[8px] inline-flex items-center justify-center shrink-0" title="Closed">/</span>;
  }
}
