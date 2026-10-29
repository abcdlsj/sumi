// The accent a Space picked at creation. Clarity keeps colour for signals, so
// the accent is used sparingly: the Space mark, primary actions, the active
// navigation marker and links. Derived shades keep text readable on white.

const INK = "#1c1b19";
const WHITE = "#ffffff";

type Rgb = [number, number, number];

function parse(hex: string): Rgb | undefined {
  const match = /^#?([0-9a-f]{6})$/i.exec(hex.trim());
  if (!match) return undefined;
  const value = Number.parseInt(match[1], 16);
  return [(value >> 16) & 255, (value >> 8) & 255, value & 255];
}

function toHex([r, g, b]: Rgb): string {
  return `#${[r, g, b].map((channel) => Math.round(channel).toString(16).padStart(2, "0")).join("")}`;
}

function luminance([r, g, b]: Rgb): number {
  const linear = (channel: number) => {
    const c = channel / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b);
}

export function contrast(a: string, b: string): number {
  const [la, lb] = [luminance(parse(a)!), luminance(parse(b)!)];
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

function mix(a: Rgb, b: Rgb, amount: number): Rgb {
  return [0, 1, 2].map((index) => a[index] + (b[index] - a[index]) * amount) as Rgb;
}

export interface AccentTokens {
  "--accent": string;
  "--accent-hover": string;
  "--accent-ink": string;
  "--accent-soft": string;
  "--on-accent": string;
}

export function accentTokens(accent: string): AccentTokens | undefined {
  const rgb = parse(accent);
  if (!rgb) return undefined;
  const base = toHex(rgb);
  // Text in the accent colour on white: darken until it reads at body size.
  let ink = rgb;
  for (let step = 1; step <= 10 && contrast(toHex(ink), WHITE) < 4.5; step += 1) ink = mix(rgb, parse(INK)!, step / 10);
  return {
    "--accent": base,
    "--accent-hover": toHex(mix(rgb, parse(INK)!, 0.12)),
    "--accent-ink": toHex(ink),
    "--accent-soft": toHex(mix(rgb, parse(WHITE)!, 0.88)),
    // Buttons carry short bold labels and icons, so 3:1 is enough for white.
    "--on-accent": contrast(base, WHITE) >= 3 ? WHITE : INK,
  };
}

/** Put a Space's accent on the document so dialogs and popovers share it. */
export function applySpaceAccent(accent: string | undefined): () => void {
  const root = document.documentElement;
  const tokens = accent ? accentTokens(accent) : undefined;
  if (!tokens) return () => undefined;
  for (const [name, value] of Object.entries(tokens)) root.style.setProperty(name, value);
  return () => {
    for (const name of Object.keys(tokens)) root.style.removeProperty(name);
  };
}
