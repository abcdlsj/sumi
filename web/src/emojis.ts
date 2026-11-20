export type ReactionEmoji = "thumbsup" | "eyes" | "check" | "rocket" | "fire" | "sparkles" | "thinking" | "party";

export interface EmojiDef {
  code: ReactionEmoji;
  label: string;
  glyph: string;
}

export const REACTION_EMOJIS: EmojiDef[] = [
  { code: "thumbsup", label: "Agree / LGTM", glyph: "👍" },
  { code: "eyes", label: "Looking into it", glyph: "👀" },
  { code: "check", label: "Done / Verified", glyph: "✅" },
  { code: "rocket", label: "Shipped", glyph: "🚀" },
  { code: "fire", label: "Awesome", glyph: "🔥" },
  { code: "sparkles", label: "Nice touch", glyph: "✨" },
  { code: "thinking", label: "Thinking", glyph: "🤔" },
  { code: "party", label: "Celebrate", glyph: "🎉" },
];

const BY_CODE = new Map<string, EmojiDef>(REACTION_EMOJIS.map((item) => [item.code, item]));

export function getReactionEmoji(code: string): EmojiDef {
  return BY_CODE.get(code) ?? { code: code as ReactionEmoji, label: code, glyph: "✨" };
}
