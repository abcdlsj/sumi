export type SpaceAccent = "flame" | "gold" | "rose" | "iris" | "sky";

/** Swatches shown in pickers; the full light/dark palettes live in index.css under [data-accent]. */
export const SPACE_ACCENTS: { value: SpaceAccent; label: string; swatch: string }[] = [
  { value: "flame", label: "Flame", swatch: "#f0602f" },
  { value: "gold", label: "Gold", swatch: "#e8b42d" },
  { value: "rose", label: "Rose", swatch: "#e0457b" },
  { value: "iris", label: "Iris", swatch: "#6c5ce7" },
  { value: "sky", label: "Sky", swatch: "#2f80ed" },
];

export function accentSwatch(accent?: string): string {
  return (SPACE_ACCENTS.find((a) => a.value === accent) ?? SPACE_ACCENTS[0]).swatch;
}

/** Puts the active Space's accent on <html> so portals (dialogs, toasts) share it. */
export function applyAccent(accent?: string): void {
  const root = document.documentElement;
  if (accent && accent !== "flame" && SPACE_ACCENTS.some((a) => a.value === accent)) root.dataset.accent = accent;
  else delete root.dataset.accent;
}
