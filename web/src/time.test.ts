import { describe, expect, it } from "vitest";

import { formatClock, formatDateTime, formatDayDivider, formatLongDate, formatRecent } from "./time";

const noon = new Date(2026, 9, 29, 11, 47);

describe("time formatting", () => {
  it("formats a clock and a day with the clock", () => {
    expect(formatClock(noon.toISOString())).toMatch(/^11:47/);
    expect(formatDateTime(noon.toISOString())).toMatch(/^Oct 29, 11:47/);
  });

  it("adds the year for a date outside the current year", () => {
    expect(formatLongDate(new Date(2025, 11, 31).toISOString())).toBe("Dec 31, 2025");
  });

  it("shows only the clock for a moment from today", () => {
    expect(formatRecent(noon.toISOString(), noon)).toMatch(/^11:47/);
    expect(formatRecent(new Date(2026, 9, 27, 8, 5).toISOString(), noon)).toBe("Oct 27, 08:05 AM");
  });

  it("returns an empty string for a missing or invalid value", () => {
    expect(formatRecent(null)).toBe("");
    expect(formatDateTime("not a date")).toBe("");
  });
});

describe("day divider", () => {
  it("names today and yesterday, then the long day and month", () => {
    expect(formatDayDivider(noon.toISOString(), noon)).toBe("Today");
    expect(formatDayDivider(new Date(2026, 9, 28, 9, 0).toISOString(), noon)).toBe("Yesterday");
    expect(formatDayDivider(new Date(2026, 9, 2, 9, 0).toISOString(), noon)).toBe("October 2");
    expect(formatDayDivider(new Date(2025, 9, 2, 9, 0).toISOString(), noon)).toBe("October 2, 2025");
  });
});

describe("fixed locale", () => {
  it("formats in English whatever the browser language is", () => {
    expect(formatDayDivider(new Date(2026, 9, 2, 9, 0).toISOString(), noon)).toBe("October 2");
    expect(formatClock(new Date(2026, 9, 2, 9, 5).toISOString())).toBe("09:05 AM");
  });
});
