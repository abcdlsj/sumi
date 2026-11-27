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

export async function openDesktopURL(bridge: DesktopBridge, targetUrl: string): Promise<void> {
  const res = await fetch(`${bridge.controlUrl}/desktop/open-url`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${bridge.token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ url: targetUrl }),
  });
  if (!res.ok) {
    throw new Error("Unable to open URL in browser");
  }
}

/**
 * Resolves an anchor element's href if it should be opened in the OS default browser
 * instead of navigating inside the desktop WebView.
 */
export function resolveExternalHref(anchor: HTMLAnchorElement, currentHref: string = window.location.href): string | null {
  if (anchor.hasAttribute("download")) return null;
  const raw = anchor.getAttribute("href")?.trim();
  if (!raw || raw.startsWith("#")) return null;
  let parsed: URL;
  let current: URL;
  try {
    current = new URL(currentHref);
    parsed = new URL(raw, current);
  } catch {
    return null;
  }
  const protocol = parsed.protocol.toLowerCase();
  if (protocol === "mailto:") {
    return parsed.href;
  }
  if (protocol !== "http:" && protocol !== "https:") {
    return null;
  }
  if (parsed.origin !== current.origin || anchor.target === "_blank") {
    return parsed.href;
  }
  return null;
}

/**
 * In the desktop shell, intercepts external anchor clicks (including `target="_blank"`
 * links that WKWebView otherwise drops) and delegates them to the OS default browser.
 */
export function installExternalLinkHandler(target: Window = window): () => void {
  const handle = (e: MouseEvent, expectedButton: number) => {
    if (e.defaultPrevented || e.button !== expectedButton) return;
    const bridge = getDesktopBridge();
    if (!bridge) return;
    const node = e.target as Node | null;
    const el = node && node.nodeType === 1 ? (node as HTMLElement) : node?.parentElement;
    if (!el || typeof el.closest !== "function") return;
    const anchor = el.closest("a[href]") as HTMLAnchorElement | null;
    if (!anchor) return;
    const externalUrl = resolveExternalHref(anchor, target.location.href);
    if (!externalUrl) return;
    e.preventDefault();
    void openDesktopURL(bridge, externalUrl).catch(() => {
      // Ignore bridge errors; the desktop shell logs failures locally.
    });
  };
  const onClick = (e: MouseEvent) => handle(e, 0);
  const onAuxClick = (e: MouseEvent) => handle(e, 1);
  target.addEventListener("click", onClick);
  target.addEventListener("auxclick", onAuxClick);
  return () => {
    target.removeEventListener("click", onClick);
    target.removeEventListener("auxclick", onAuxClick);
  };
}
