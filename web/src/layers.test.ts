import { beforeEach, describe, expect, it, vi } from "vitest";
import { dismissTopLayer, isTopLayer, layerDepth, pushLayer, releaseLayer, resetLayers } from "./layers";

describe("layer stack", () => {
  beforeEach(() => resetLayers());

  it("dismisses overlays in reverse registration order", () => {
    const closeFirst = vi.fn();
    const closeSecond = vi.fn();
    pushLayer(closeFirst);
    pushLayer(closeSecond);

    expect(dismissTopLayer()).toBe(true);
    expect(closeSecond).toHaveBeenCalledTimes(1);
    expect(closeFirst).not.toHaveBeenCalled();

    expect(dismissTopLayer()).toBe(true);
    expect(closeFirst).toHaveBeenCalledTimes(1);
  });

  it("reports an empty stack so callers can fall through to the window", () => {
    expect(dismissTopLayer()).toBe(false);
  });

  it("tracks the topmost layer by token and survives out-of-order releases", () => {
    const bottom = pushLayer(() => {});
    const top = pushLayer(() => {});
    expect(isTopLayer(top)).toBe(true);
    expect(isTopLayer(bottom)).toBe(false);

    releaseLayer(bottom);
    expect(layerDepth()).toBe(1);
    expect(isTopLayer(top)).toBe(true);

    releaseLayer(top);
    releaseLayer(top);
    expect(layerDepth()).toBe(0);
  });
});
