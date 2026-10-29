// Humans read as soft tinted initials; Agents share one neutral seal ground
// and differ by ink, so the two kinds never blur together in a list.
const HUMAN_PALETTES = [
  { background: "#EEEAFB", foreground: "#5547A8" },
  { background: "#E6F2EA", foreground: "#2F6E47" },
  { background: "#FBEDE4", foreground: "#9A4B22" },
  { background: "#E5EFF8", foreground: "#2D5E8C" },
  { background: "#F7E9EF", foreground: "#94385E" },
  { background: "#F1EFE6", foreground: "#5E5640" },
] as const;

const AGENT_SEAL_BACKGROUND = "#F1F0EC";
const AGENT_INKS = [
  "#3A5FC8",
  "#2B2A27",
  "#A8592C",
  "#2E7D6F",
  "#7A4FB5",
  "#B5434F",
  "#4F7A2E",
  "#8A6A1F",
] as const;

export interface AgentIdenticon {
  background: string;
  foreground: string;
  cells: ReadonlyArray<readonly [number, number]>;
  signature: string;
}

export function buildAgentIdenticon(memberId: string): AgentIdenticon {
  const palette = {
    background: AGENT_SEAL_BACKGROUND,
    foreground: AGENT_INKS[stableHash(memberId, 0xc2b2ae35) % AGENT_INKS.length],
  };
  const cells = pickSealCells(memberId);
  const orderedCells = [...cells]
    .map((cell) => cell.split(",").map(Number) as [number, number])
    .sort(([leftX, leftY], [rightX, rightY]) => leftY - rightY || leftX - rightX);
  const signature = `${palette.foreground}:${orderedCells.map(([x, y]) => `${x}${y}`).join("")}`;
  return { ...palette, cells: orderedCells, signature };
}

// A seal is an 8x8 grid mirrored around its centre: each of the 32 cells on
// the left half is filled about half the time. Draws that come out too sparse,
// too heavy or split in two are rejected so every seal reads as one mark.
function pickSealCells(memberId: string): Set<string> {
  let fallback = new Set<string>();
  for (let attempt = 0; attempt < 8; attempt += 1) {
    let state = stableHash(memberId, 0x9e3779b9 + attempt) || 1;
    const next = () => {
      state ^= state << 13;
      state >>>= 0;
      state ^= state >>> 17;
      state ^= state << 5;
      state >>>= 0;
      return state;
    };
    const cells = new Set<string>();
    let filled = 0;
    let centre = 0;
    let rows = 0;
    for (let y = 0; y < 8; y += 1) {
      let rowFilled = false;
      for (let x = 0; x < 4; x += 1) {
        if (next() % 100 >= 52) continue;
        addMirroredCell(cells, x, y);
        filled += 1;
        rowFilled = true;
        if (x === 3) centre += 1;
      }
      if (rowFilled) rows += 1;
    }
    // Lone pixels read as noise at 22px; drop any cell with no edge neighbour.
    for (const cell of [...cells]) {
      const [x, y] = cell.split(",").map(Number);
      if (![[x - 1, y], [x + 1, y], [x, y - 1], [x, y + 1]].some(([nx, ny]) => cells.has(`${nx},${ny}`))) {
        cells.delete(cell);
        if (x < 4) filled -= 1;
      }
    }
    if (attempt === 0) fallback = cells;
    if (filled >= 12 && filled <= 22 && centre >= 3 && rows >= 6) return cells;
  }
  return fallback;
}

export function identityPalette(memberId: string) {
  return HUMAN_PALETTES[stableHash(memberId, 0xc2b2ae35) % HUMAN_PALETTES.length];
}

function addMirroredCell(cells: Set<string>, x: number, y: number) {
  cells.add(`${x},${y}`);
  cells.add(`${7 - x},${y}`);
}

function stableHash(value: string, salt: number): number {
  let hash = (0x811c9dc5 ^ salt) >>> 0;
  for (const character of value) {
    hash ^= character.codePointAt(0)!;
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return mix(hash);
}

function mix(value: number): number {
  let mixed = value >>> 0;
  mixed ^= mixed >>> 16;
  mixed = Math.imul(mixed, 0x7feb352d) >>> 0;
  mixed ^= mixed >>> 15;
  mixed = Math.imul(mixed, 0x846ca68b) >>> 0;
  return (mixed ^ (mixed >>> 16)) >>> 0;
}
