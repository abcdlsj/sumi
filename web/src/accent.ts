export type SpaceAccent = "flame" | "gold" | "rose" | "iris" | "sky";

/** Swatches shown in pickers; the full light/dark palettes live in index.css under [data-accent]. */
export const SPACE_ACCENTS: { value: SpaceAccent; label: string; swatch: string; fg: string; light: string; dark: string }[] = [
  { value: "flame", label: "Flame", swatch: "#f0602f", fg: "#ffffff", light: "#ff885c", dark: "#cc4217" },
  { value: "gold", label: "Gold", swatch: "#e8b42d", fg: "#1c1b19", light: "#f7cf5e", dark: "#c69214" },
  { value: "rose", label: "Rose", swatch: "#e0457b", fg: "#ffffff", light: "#f4739f", dark: "#b62658" },
  { value: "iris", label: "Iris", swatch: "#6c5ce7", fg: "#ffffff", light: "#9183f5", dark: "#4e3ec2" },
  { value: "sky", label: "Sky", swatch: "#2f80ed", fg: "#ffffff", light: "#5ca0f7", dark: "#1b5ec2" },
];

export function accentSwatch(accent?: string): string {
  return (SPACE_ACCENTS.find((a) => a.value === accent) ?? SPACE_ACCENTS[0]).swatch;
}

export function accentForeground(accent?: string): string {
  return (SPACE_ACCENTS.find((a) => a.value === accent) ?? SPACE_ACCENTS[0]).fg;
}

export function accentGradient(accent?: string): string {
  const a = SPACE_ACCENTS.find((item) => item.value === accent) ?? SPACE_ACCENTS[0];
  return `linear-gradient(180deg, ${a.light} 0%, ${a.swatch} 48%, ${a.dark} 100%)`;
}

/** Puts the active Space's accent on <html> so portals (dialogs, toasts) share it. */
export function applyAccent(accent?: string): void {
  const root = document.documentElement;
  if (accent && accent !== "flame" && SPACE_ACCENTS.some((a) => a.value === accent)) root.dataset.accent = accent;
  else delete root.dataset.accent;
}
