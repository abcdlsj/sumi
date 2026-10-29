// Office movement: a coarse walkability grid over the room, A* paths around
// furniture, and walkers that follow those paths at a constant pace. Pure
// functions only, so the behaviour can be tested without rendering.

export interface Point {
  x: number;
  y: number;
}

export interface Rect {
  x0: number;
  y0: number;
  x1: number;
  y1: number;
}

export interface OfficeGrid {
  cell: number;
  cols: number;
  rows: number;
  blocked: Uint8Array;
}

export const CELL = 8;
/** Room pixels per second; slow enough to read as strolling. */
export const WALK_SPEED = 34;

export function buildGrid(width: number, height: number, obstacles: readonly Rect[], cell = CELL): OfficeGrid {
  const cols = Math.ceil(width / cell);
  const rows = Math.ceil(height / cell);
  const blocked = new Uint8Array(cols * rows);
  for (let row = 0; row < rows; row += 1) {
    for (let col = 0; col < cols; col += 1) {
      const x = col * cell + cell / 2;
      const y = row * cell + cell / 2;
      // Keep a margin around the walls so nobody walks along the room edge.
      const edge = x < 8 || x > width - 8 || y > height - 6;
      if (edge || obstacles.some((box) => x >= box.x0 && x <= box.x1 && y >= box.y0 && y <= box.y1)) {
        blocked[row * cols + col] = 1;
      }
    }
  }
  return { cell, cols, rows, blocked };
}

function cellOf(grid: OfficeGrid, point: Point): number {
  const col = Math.max(0, Math.min(grid.cols - 1, Math.floor(point.x / grid.cell)));
  const row = Math.max(0, Math.min(grid.rows - 1, Math.floor(point.y / grid.cell)));
  return row * grid.cols + col;
}

function centreOf(grid: OfficeGrid, index: number): Point {
  return {
    x: (index % grid.cols) * grid.cell + grid.cell / 2,
    y: Math.floor(index / grid.cols) * grid.cell + grid.cell / 2,
  };
}

/** The nearest walkable cell to a point, searching outwards ring by ring. */
function nearestOpen(grid: OfficeGrid, index: number): number {
  if (!grid.blocked[index]) return index;
  const col = index % grid.cols;
  const row = Math.floor(index / grid.cols);
  for (let radius = 1; radius < Math.max(grid.cols, grid.rows); radius += 1) {
    for (let dy = -radius; dy <= radius; dy += 1) {
      for (let dx = -radius; dx <= radius; dx += 1) {
        if (Math.max(Math.abs(dx), Math.abs(dy)) !== radius) continue;
        const c = col + dx;
        const r = row + dy;
        if (c < 0 || r < 0 || c >= grid.cols || r >= grid.rows) continue;
        const candidate = r * grid.cols + c;
        if (!grid.blocked[candidate]) return candidate;
      }
    }
  }
  return index;
}

/**
 * A path from `from` to `to` that avoids blocked cells. Start and goal may sit
 * inside furniture (a desk chair, a table seat): the walker steps out to the
 * nearest open cell, travels, then steps in. Returns waypoints after `from`,
 * ending exactly at `to`.
 */
export function findPath(grid: OfficeGrid, from: Point, to: Point): Point[] {
  const start = nearestOpen(grid, cellOf(grid, from));
  const goal = nearestOpen(grid, cellOf(grid, to));
  if (start === goal) return [to];

  const size = grid.cols * grid.rows;
  const cost = new Float64Array(size).fill(Infinity);
  const previous = new Int32Array(size).fill(-1);
  const closed = new Uint8Array(size);
  const open: number[] = [start];
  const score = new Float64Array(size).fill(Infinity);
  cost[start] = 0;
  const goalPoint = centreOf(grid, goal);
  const heuristic = (index: number) => {
    const point = centreOf(grid, index);
    return Math.hypot(point.x - goalPoint.x, point.y - goalPoint.y) / grid.cell;
  };
  score[start] = heuristic(start);

  while (open.length) {
    let best = 0;
    for (let index = 1; index < open.length; index += 1) {
      if (score[open[index]] < score[open[best]]) best = index;
    }
    const current = open.splice(best, 1)[0];
    if (current === goal) break;
    closed[current] = 1;
    const col = current % grid.cols;
    const row = Math.floor(current / grid.cols);
    for (let dy = -1; dy <= 1; dy += 1) {
      for (let dx = -1; dx <= 1; dx += 1) {
        if (!dx && !dy) continue;
        const c = col + dx;
        const r = row + dy;
        if (c < 0 || r < 0 || c >= grid.cols || r >= grid.rows) continue;
        const next = r * grid.cols + c;
        if (closed[next] || grid.blocked[next]) continue;
        // No corner cutting past furniture.
        if (dx && dy && (grid.blocked[row * grid.cols + c] || grid.blocked[r * grid.cols + col])) continue;
        const step = dx && dy ? Math.SQRT2 : 1;
        const tentative = cost[current] + step;
        if (tentative >= cost[next]) continue;
        cost[next] = tentative;
        previous[next] = current;
        score[next] = tentative + heuristic(next);
        if (!open.includes(next)) open.push(next);
      }
    }
  }

  if (previous[goal] === -1) return [to];
  const cells: number[] = [];
  for (let index = goal; index !== start; index = previous[index]) cells.push(index);
  cells.reverse();
  // Simplify from the walker's real position to the real target, so nobody
  // detours through a cell centre when the straight line is clear.
  const points = simplify(grid, [from, ...cells.map((index) => centreOf(grid, index)), to]);
  return points.slice(1);
}

/** Drop waypoints that a straight walk between their neighbours already covers. */
function simplify(grid: OfficeGrid, points: Point[]): Point[] {
  if (points.length < 3) return points;
  const kept: Point[] = [points[0]];
  let anchor = points[0];
  for (let index = 1; index < points.length - 1; index += 1) {
    if (!clearLine(grid, anchor, points[index + 1])) {
      kept.push(points[index]);
      anchor = points[index];
    }
  }
  kept.push(points[points.length - 1]);
  return kept;
}

function clearLine(grid: OfficeGrid, from: Point, to: Point): boolean {
  const steps = Math.ceil(Math.hypot(to.x - from.x, to.y - from.y) / (grid.cell / 2));
  for (let step = 1; step < steps; step += 1) {
    const t = step / steps;
    if (grid.blocked[cellOf(grid, { x: from.x + (to.x - from.x) * t, y: from.y + (to.y - from.y) * t })]) return false;
  }
  return true;
}

export interface Walker {
  x: number;
  y: number;
  path: Point[];
  /** -1 faces left, 1 faces right. */
  facing: -1 | 1;
  target: Point;
}

export function spawnWalker(at: Point): Walker {
  return { x: at.x, y: at.y, path: [], facing: 1, target: at };
}

export function routeWalker(grid: OfficeGrid, walker: Walker, target: Point): Walker {
  if (samePoint(walker.target, target)) return walker;
  if (samePoint(walker, target)) return { ...walker, path: [], target };
  return { ...walker, path: findPath(grid, walker, target), target };
}

/** Advance a walker along its path by `seconds` at `speed`. */
export function stepWalker(walker: Walker, seconds: number, speed = WALK_SPEED): Walker {
  if (!walker.path.length) return walker;
  let budget = speed * seconds;
  let { x, y, facing } = walker;
  const path = [...walker.path];
  while (budget > 0 && path.length) {
    const next = path[0];
    const dx = next.x - x;
    const dy = next.y - y;
    const length = Math.hypot(dx, dy);
    if (Math.abs(dx) > 0.5) facing = dx < 0 ? -1 : 1;
    if (length <= budget) {
      x = next.x;
      y = next.y;
      budget -= length;
      path.shift();
    } else {
      x += (dx / length) * budget;
      y += (dy / length) * budget;
      budget = 0;
    }
  }
  return { ...walker, x, y, facing, path };
}

export function isMoving(walker: Walker): boolean {
  return walker.path.length > 0;
}

export function samePoint(a: Point, b: Point): boolean {
  return Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5;
}
