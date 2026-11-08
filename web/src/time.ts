// One time language for the UI: a clock where the day is known, the day and month otherwise,
// and the same pattern in every list, rail and header. Screens call these instead of building
// their own Intl options, so a timestamp reads the same on every page.
//
// The locale is fixed: the interface copy is English, so a browser set to another language must
// not mix "10月29日" into a sentence that says "Updated". It also keeps output stable in tests.
const LOCALE = "en-US";

const dateTimeFormat = new Intl.DateTimeFormat(LOCALE, {
  month: "short",
  day: "numeric",
  hour: "2-digit",
  minute: "2-digit",
});

const clockFormat = new Intl.DateTimeFormat(LOCALE, { hour: "2-digit", minute: "2-digit" });

const dateFormat = new Intl.DateTimeFormat(LOCALE, { month: "short", day: "numeric" });

const longDateFormat = new Intl.DateTimeFormat(LOCALE, {
  year: "numeric",
  month: "short",
  day: "numeric",
});

const fullDateFormat = new Intl.DateTimeFormat(LOCALE, { dateStyle: "full", timeStyle: "short" });

const dayDividerFormat = new Intl.DateTimeFormat(LOCALE, { month: "long", day: "numeric" });

const dayDividerYearFormat = new Intl.DateTimeFormat(LOCALE, {
  month: "long",
  day: "numeric",
  year: "numeric",
});

function parse(value: string | null | undefined): Date | null {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
}

function sameDay(left: Date, right: Date): boolean {
  return (
    left.getFullYear() === right.getFullYear() &&
    left.getMonth() === right.getMonth() &&
    left.getDate() === right.getDate()
  );
}

/** "11:47 AM" */
export function formatClock(value: string | null | undefined): string {
  const date = parse(value);
  return date ? clockFormat.format(date) : "";
}

/** "Oct 29, 11:47 AM" */
export function formatDateTime(value: string | null | undefined): string {
  const date = parse(value);
  return date ? dateTimeFormat.format(date) : "";
}

/** "Oct 29" */
export function formatDay(value: string | null | undefined): string {
  const date = parse(value);
  return date ? dateFormat.format(date) : "";
}

/** "Oct 29, 2026" */
export function formatLongDate(value: string | null | undefined): string {
  const date = parse(value);
  return date ? longDateFormat.format(date) : "";
}

/** "Thursday, October 29, 2026 at 11:47 AM" — for the title of a timestamp. */
export function formatFullDateTime(value: string | null | undefined): string {
  const date = parse(value);
  return date ? fullDateFormat.format(date) : "";
}

/** The clock when the moment is today, otherwise the day it happened on, then the clock. */
export function formatRecent(value: string | null | undefined, now = new Date()): string {
  const date = parse(value);
  if (!date) return "";
  return sameDay(date, now) ? clockFormat.format(date) : dateTimeFormat.format(date);
}

/** The label above a day of messages: "Today", "Yesterday", "October 29". */
export function formatDayDivider(value: string | null | undefined, now = new Date()): string {
  const date = parse(value);
  if (!date) return "";
  if (sameDay(date, now)) return "Today";
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(date, yesterday)) return "Yesterday";
  return date.getFullYear() === now.getFullYear()
    ? dayDividerFormat.format(date)
    : dayDividerYearFormat.format(date);
}
