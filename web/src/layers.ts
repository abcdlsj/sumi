/**
 * Dismissible overlay stack for the web shell.
 *
 * Dialogs, lightboxes and previews push a layer while they are open. The desktop
 * shell hands ⌘W / Ctrl+W to {@link dismissTopLayer} so the shortcut closes the
 * topmost overlay instead of hiding the window, and only falls through to the
 * window when nothing is open.
 */

interface Layer {
  token: symbol;
  close: () => void;
}

const stack: Layer[] = [];

/** Registers an overlay and returns its token. Layers close in reverse registration order. */
export function pushLayer(close: () => void): symbol {
  const token = Symbol("layer");
  stack.push({ token, close });
  return token;
}

/** True when `token` is the overlay that should receive Escape and close shortcuts. */
export function isTopLayer(token: symbol): boolean {
  return stack.length > 0 && stack[stack.length - 1].token === token;
}

/** Removes a layer; safe to call for an already-released token. */
export function releaseLayer(token: symbol): void {
  const idx = stack.findIndex((entry) => entry.token === token);
  if (idx >= 0) stack.splice(idx, 1);
}

/** Closes the topmost overlay and reports whether one was open. */
export function dismissTopLayer(): boolean {
  const layer = stack[stack.length - 1];
  if (!layer) return false;
  stack.pop();
  layer.close();
  return true;
}

/** Number of registered overlays (test helper). */
export function layerDepth(): number {
  return stack.length;
}

/** Drops every registered layer (test helper). */
export function resetLayers(): void {
  stack.length = 0;
}
