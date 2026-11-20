export type ReactionEmoji = "thumbsup" | "eyes" | "check" | "rocket" | "fire" | "sparkles" | "thinking" | "party";

export interface EmojiDef {
  code: ReactionEmoji;
  label: string;
  /** SVG asset under web/public/emoji, so reactions render the same on every OS. */
  src: string;
}

// Artwork is Twemoji (github.com/jdecked/twemoji), CC-BY 4.0.
export const REACTION_EMOJIS: EmojiDef[] = [
  { code: "thumbsup", label: "Agree / LGTM", src: "/emoji/1f44d.svg" },
  { code: "eyes", label: "Looking into it", src: "/emoji/1f440.svg" },
  { code: "check", label: "Done / Verified", src: "/emoji/2705.svg" },
  { code: "rocket", label: "Shipped", src: "/emoji/1f680.svg" },
  { code: "fire", label: "Awesome", src: "/emoji/1f525.svg" },
  { code: "sparkles", label: "Nice touch", src: "/emoji/2728.svg" },
  { code: "thinking", label: "Thinking", src: "/emoji/1f914.svg" },
  { code: "party", label: "Celebrate", src: "/emoji/1f389.svg" },
];

const BY_CODE = new Map<string, EmojiDef>(REACTION_EMOJIS.map((item) => [item.code, item]));

const FALLBACK: EmojiDef = { code: "sparkles", label: "sparkles", src: "/emoji/2728.svg" };

export function getReactionEmoji(code: string): EmojiDef {
  return BY_CODE.get(code) ?? { ...FALLBACK, code: code as ReactionEmoji, label: code };
}
