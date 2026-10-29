import { describe, expect, it } from "vitest";
import { accentTokens, contrast } from "./spaceAccent";

describe("accentTokens", () => {
  it.each(["#F0602F", "#E8B42D", "#3C9E8F", "#6C5CE7"])("keeps %s readable", (accent) => {
    const tokens = accentTokens(accent)!;
    expect(tokens["--accent"]).toBe(accent.toLowerCase());
    expect(contrast(tokens["--accent-ink"], "#ffffff")).toBeGreaterThanOrEqual(4.5);
    expect(contrast(tokens["--accent"], tokens["--on-accent"])).toBeGreaterThanOrEqual(3);
  });

  it("puts dark text on a light accent", () => {
    expect(accentTokens("#E8B42D")!["--on-accent"]).toBe("#1c1b19");
    expect(accentTokens("#6C5CE7")!["--on-accent"]).toBe("#ffffff");
  });

  it("ignores values that are not hex colours", () => {
    expect(accentTokens("tomato")).toBeUndefined();
  });
});
