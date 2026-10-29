import { describe, expect, it } from "vitest";
import { buildGrid, findPath, routeWalker, spawnWalker, stepWalker, type Point } from "./officeSim";

const wall = { x0: 60, y0: 0, x1: 100, y1: 150 };

function crossesWall(from: Point, path: Point[]) {
  let previous = from;
  for (const point of path) {
    for (let t = 0; t <= 1; t += 0.05) {
      const x = previous.x + (point.x - previous.x) * t;
      const y = previous.y + (point.y - previous.y) * t;
      if (x > wall.x0 && x < wall.x1 && y > wall.y0 && y < wall.y1) return true;
    }
    previous = point;
  }
  return false;
}

describe("officeSim", () => {
  it("routes around furniture and ends exactly on the target", () => {
    const grid = buildGrid(160, 200, [wall]);
    const from = { x: 30, y: 40 };
    const to = { x: 130, y: 40 };
    const path = findPath(grid, from, to);
    expect(path[path.length - 1]).toEqual(to);
    expect(crossesWall(from, path)).toBe(false);
    expect(path.some((point) => point.y > wall.y1)).toBe(true);
  });

  it("walks a routed walker to its target at a constant pace", () => {
    const grid = buildGrid(160, 200, []);
    let walker = routeWalker(grid, spawnWalker({ x: 20, y: 40 }), { x: 120, y: 40 });
    walker = stepWalker(walker, 1, 34);
    expect(walker.x).toBeCloseTo(54, 0);
    expect(walker.facing).toBe(1);
    for (let tick = 0; tick < 10; tick += 1) walker = stepWalker(walker, 1, 34);
    expect(walker).toMatchObject({ x: 120, y: 40, path: [] });
  });

  it("keeps the same walker when the target has not changed", () => {
    const grid = buildGrid(160, 200, []);
    const walker = routeWalker(grid, spawnWalker({ x: 20, y: 40 }), { x: 120, y: 40 });
    expect(routeWalker(grid, walker, { x: 120, y: 40 })).toBe(walker);
  });
});
