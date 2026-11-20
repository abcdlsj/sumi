const SHELL_KEY = "sumi.shell";
const CONTROL_KEY = "sumi.desktop.control";
const TOKEN_KEY = "sumi.desktop.token";

export interface DesktopBridge {
  controlUrl: string;
  token: string;
}

export interface DesktopServerState {
  server_url: string;
  default_local_url: string;
  is_local: boolean;
  computer_status: string;
}

/** Moves `?shell=desktop` and `#desktop_control=...&desktop_token=...` into sessionStorage and strips them from the URL. */
export function captureShellFromURL(): void {
  const url = new URL(window.location.href);
  let dirty = false;

  if (url.searchParams.get("shell") === "desktop") {
    sessionStorage.setItem(SHELL_KEY, "desktop");
    url.searchParams.delete("shell");
    dirty = true;
  }

  if (url.hash.length > 1) {
    const hashParams = new URLSearchParams(url.hash.slice(1));
    const control = hashParams.get("desktop_control");
    const token = hashParams.get("desktop_token");
    if (control && token) {
      sessionStorage.setItem(CONTROL_KEY, control);
      sessionStorage.setItem(TOKEN_KEY, token);
      hashParams.delete("desktop_control");
      hashParams.delete("desktop_token");
      const rest = hashParams.toString();
      url.hash = rest ? `#${rest}` : "";
      dirty = true;
    }
  }

  if (dirty) {
    window.history.replaceState(null, "", url.pathname + url.search + url.hash);
  }
  document.documentElement.classList.toggle("desktop", sessionStorage.getItem(SHELL_KEY) === "desktop");
}

export function isDesktopShell(): boolean {
  return sessionStorage.getItem(SHELL_KEY) === "desktop";
}

export function getDesktopBridge(): DesktopBridge | null {
  const controlUrl = sessionStorage.getItem(CONTROL_KEY);
  const token = sessionStorage.getItem(TOKEN_KEY);
  if (!controlUrl || !token) return null;
  return { controlUrl, token };
}

export function formatServerLabel(serverUrl: string, isLocal: boolean): string {
  if (isLocal) return "Local";
  try {
    return new URL(serverUrl).host;
  } catch {
    return serverUrl;
  }
}

export async function fetchDesktopServer(bridge: DesktopBridge): Promise<DesktopServerState> {
  const res = await fetch(`${bridge.controlUrl}/desktop/server`, {
    headers: { Authorization: `Bearer ${bridge.token}` },
  });
  if (!res.ok) {
    throw new Error("Unable to read desktop server state");
  }
  return (await res.json()) as DesktopServerState;
}

export async function switchDesktopServer(bridge: DesktopBridge, serverUrl: string): Promise<DesktopServerState> {
  const res = await fetch(`${bridge.controlUrl}/desktop/server`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${bridge.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ server_url: serverUrl }),
  });
  const body = (await res.json().catch(() => ({}))) as { message?: string } & Partial<DesktopServerState>;
  if (!res.ok) {
    throw new Error(body.message || "Unable to switch server");
  }
  return body as DesktopServerState;
}
