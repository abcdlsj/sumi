import { afterEach, describe, expect, it } from "vitest";
import { applySpellCheck, clearPreferencesForTests, readSpellCheck, writeSpellCheck } from "./preferences";

afterEach(() => {
  clearPreferencesForTests();
  applySpellCheck("on");
});

describe("spellcheck preference", () => {
  it("defaults to on and persists off/on with root DOM attributes", () => {
    expect(readSpellCheck()).toBe("on");
    applySpellCheck();
    expect(document.documentElement.dataset.spellcheck).toBeUndefined();
    expect(document.documentElement.getAttribute("spellcheck")).toBeNull();

    writeSpellCheck("off");
    expect(readSpellCheck()).toBe("off");
    expect(document.documentElement.dataset.spellcheck).toBe("off");
    expect(document.documentElement.getAttribute("spellcheck")).toBe("false");

    writeSpellCheck("on");
    expect(readSpellCheck()).toBe("on");
    expect(document.documentElement.dataset.spellcheck).toBeUndefined();
    expect(document.documentElement.getAttribute("spellcheck")).toBeNull();
  });
});
