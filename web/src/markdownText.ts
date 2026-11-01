// Message bodies are Markdown, but the places that show them as plain words (Inbox rows, Thread
// reply previews, Agent activity, the Task Result) must not show the syntax. Rendered bodies
// still go through the Markdown renderer; these helpers are only for text-only surfaces.

const PREVIEW_FENCE = /^\s*```/;
const PREVIEW_RULE = /^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/;
const PREVIEW_BLOCK_MARKER = /^\s*(?:#{1,6}\s+|>\s?|(?:[-*+]|\d+[.)])\s+)/;

/** The words of a Markdown body, with line breaks kept. */
export function plainText(body: string): string {
  return body
    .replace(/\\n/g, "\n")
    .split("\n")
    .filter((line) => !PREVIEW_FENCE.test(line) && !PREVIEW_RULE.test(line))
    .map((line) => stripInlineMarkers(line.trim().replace(PREVIEW_BLOCK_MARKER, "")))
    .join("\n")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}

/** The words of a Markdown body on one line, clipped to a preview length. */
export function plainTextPreview(body: string, maxLength = 240): string {
  const text = plainText(body).replace(/\s+/g, " ").trim();
  if (text.length <= maxLength) return text;
  const clipped = text.slice(0, maxLength);
  const lastSpace = clipped.lastIndexOf(" ");
  return `${(lastSpace > maxLength / 2 ? clipped.slice(0, lastSpace) : clipped).trimEnd()}…`;
}

function stripInlineMarkers(line: string): string {
  return line
    .replace(/!\[([^\]\n]*)\]\([^)\s]*\)/g, "$1")
    .replace(/\[([^\]\n]+)\]\([^)\s]+\)/g, "$1")
    .replace(/\*\*([^*\n]+)\*\*/g, "$1")
    .replace(/~~([^~\n]+)~~/g, "$1")
    .replace(/(?<!\*)\*([^*\n]+)\*(?!\*)/g, "$1")
    .replace(/(?<!_)_([^_\n]+)_(?!_)/g, "$1")
    .replace(/`([^`\n]+)`/g, "$1");
}
