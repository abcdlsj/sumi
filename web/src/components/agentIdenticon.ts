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
  const shapeHash = stableHash(memberId, 0x9e3779b9);
  const detailHash = stableHash(memberId, 0x85ebca6b);
  const palette = {
    background: AGENT_SEAL_BACKGROUND,
    foreground: AGENT_INKS[stableHash(memberId, 0xc2b2ae35) % AGENT_INKS.length],
  };
  const cells = new Set<string>();

  for (let row = 0; row < 6; row += 1) {
    const rowHash = mix(shapeHash ^ Math.imul(detailHash, row + 1));
    const width = 1 + (rowHash % 3);
    const offset = (rowHash >>> 8) % (4 - width);
    for (let column = offset; column < offset + width; column += 1) {
      addMirroredCell(cells, column + 1, row + 1);
    }
  }

  // A short central bridge keeps the generated mark visually coherent at 22px.
  const bridgeRow = 2 + (shapeHash % 3);
  addMirroredCell(cells, 3, bridgeRow);
  addMirroredCell(cells, 3, bridgeRow + 1);

  const orderedCells = [...cells]
    .map((cell) => cell.split(",").map(Number) as [number, number])
    .sort(([leftX, leftY], [rightX, rightY]) => leftY - rightY || leftX - rightX);
  const signature = `${palette.foreground}:${orderedCells.map(([x, y]) => `${x}${y}`).join("")}`;
  return { ...palette, cells: orderedCells, signature };
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
