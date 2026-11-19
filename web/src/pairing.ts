const KEY = "sumi.pairing";

export interface PendingPairing {
  id: string;
  code: string;
}

/** Moves `?pair=&code=` from the URL into sessionStorage so it survives sign-in and reloads. */
export function capturePairingFromURL(): void {
  const url = new URL(window.location.href);
  const id = url.searchParams.get("pair");
  const code = url.searchParams.get("code");
  if (!id || !code) return;
  sessionStorage.setItem(KEY, JSON.stringify({ id, code }));
  url.searchParams.delete("pair");
  url.searchParams.delete("code");
  window.history.replaceState(null, "", url.pathname + url.search + url.hash);
}

export function readPendingPairing(): PendingPairing | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as PendingPairing;
    return v.id && v.code ? v : null;
  } catch {
    return null;
  }
}

export function clearPendingPairing(): void {
  sessionStorage.removeItem(KEY);
}
