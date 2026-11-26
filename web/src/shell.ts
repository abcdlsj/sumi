const SHELL_KEY = "sumi.shell";
const CONTROL_KEY = "sumi.desktop.control";
const TOKEN_KEY = "sumi.desktop.token";
const EDITABLE_INPUT_TYPES = new Set(["", "text", "search", "url", "email", "tel", "password", "number"]);

/** True when a right-click target is an enabled text-editing control that should keep the native edit menu. */
export function isEditableTextTarget(target: EventTarget | null): boolean {
  if (!target || typeof target !== "object") return false;
  const node = target as Node;
  const el = node.nodeType === 1 ? (node as HTMLElement) : node.parentElement;
  if (!el || typeof el.closest !== "function") return false;
  const editable = el.closest("textarea, input, [contenteditable]");
  if (!editable) return false;
  if (editable instanceof HTMLTextAreaElement) {
    return !editable.disabled;
  }
  if (editable instanceof HTMLInputElement) {
    return !editable.disabled && EDITABLE_INPUT_TYPES.has((editable.type || "").toLowerCase());
  }
  if (editable instanceof HTMLElement) {
    if (editable.isContentEditable) return true;
    const attr = editable.getAttribute("contenteditable");
    return attr !== null && attr.toLowerCase() !== "false";
  }
  return false;
}

/** Suppresses the default browser context menu outside text inputs while letting Sumi menus run first. */
export function installContextMenuGuard(target: Window = window): () => void {
  const onContextMenu = (e: MouseEvent) => {
    if (e.defaultPrevented) return;
    if (isEditableTextTarget(e.target)) return;
    e.preventDefault();
  };
  target.addEventListener("contextmenu", onContextMenu);
  return () => target.removeEventListener("contextmenu", onContextMenu);
}

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
