import { describe, expect, it } from "vitest";

import { plainText, plainTextPreview } from "./markdownText";

describe("plainTextPreview", () => {
  it("removes emphasis markers and keeps the words", () => {
    expect(plainTextPreview("**What was verified** and *what was not*")).toBe(
      "What was verified and what was not",
    );
  });

  it("removes heading, list and quote markers across lines", () => {
    const body = "## Result\n1. First item\n2. Second item\n> quoted line";
    expect(plainTextPreview(body)).toBe("Result First item Second item quoted line");
  });

  it("keeps link text and drops the target", () => {
    expect(plainTextPreview("See [the review](https://example.test/r/1) for detail")).toBe(
      "See the review for detail",
    );
  });

  it("drops fences and rules but keeps code words", () => {
    const body = "Run it:\n```\nfn main() {}\n```\n---\nafter the rule";
    expect(plainTextPreview(body)).toBe("Run it: fn main() {} after the rule");
  });

  it("treats escaped newlines as line breaks", () => {
    expect(plainTextPreview(String.raw`Loose text.\n\n**Bold.**`)).toBe("Loose text. Bold.");
  });

  it("clips at a word boundary with an ellipsis", () => {
    const preview = plainTextPreview("word ".repeat(80), 40);
    expect(preview.endsWith("…")).toBe(true);
    expect(preview.length).toBeLessThanOrEqual(41);
    expect(preview).not.toContain("wor…");
  });
});

describe("plainText", () => {
  it("keeps line breaks for a multi-line body", () => {
    const body = "**Result**\n\n- Item one\n- Item two";
    expect(plainText(body)).toBe("Result\n\nItem one\nItem two");
  });
});
