export type SpaceAccent = "flame" | "gold" | "rose" | "iris" | "sky";

/** Swatches shown in pickers; the full light/dark palettes live in index.css under [data-accent]. */
export const SPACE_ACCENTS: { value: SpaceAccent; label: string; swatch: string; fg: string }[] = [
  { value: "flame", label: "Flame", swatch: "#f0602f", fg: "#ffffff" },
  { value: "gold", label: "Gold", swatch: "#e8b42d", fg: "#1c1b19" },
  { value: "rose", label: "Rose", swatch: "#e0457b", fg: "#ffffff" },
  { value: "iris", label: "Iris", swatch: "#6c5ce7", fg: "#ffffff" },
  { value: "sky", label: "Sky", swatch: "#2f80ed", fg: "#ffffff" },
];

export function accentSwatch(accent?: string): string {
  return (SPACE_ACCENTS.find((a) => a.value === accent) ?? SPACE_ACCENTS[0]).swatch;
}

export function accentForeground(accent?: string): string {
  return (SPACE_ACCENTS.find((a) => a.value === accent) ?? SPACE_ACCENTS[0]).fg;
}

/** Puts the active Space's accent on <html> so portals (dialogs, toasts) share it. */
export function applyAccent(accent?: string): void {
  const root = document.documentElement;
  if (accent && accent !== "flame" && SPACE_ACCENTS.some((a) => a.value === accent)) root.dataset.accent = accent;
  else delete root.dataset.accent;
}
