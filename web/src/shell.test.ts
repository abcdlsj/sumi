import { afterEach, describe, expect, it, vi } from "vitest";
import {
  installContextMenuGuard,
  installExternalLinkHandler,
  installWindowCloseHandler,
  isEditableTextTarget,
  requestWindowClose,
  resolveExternalHref,
  signalDesktopWindowReady,
} from "./shell";
import { pushLayer, resetLayers } from "./layers";

function dispatchContextMenu(target: EventTarget): MouseEvent {
  const event = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
  target.dispatchEvent(event);
  return event;
}

describe("installContextMenuGuard", () => {
  it("suppresses the browser context menu on regular UI surfaces and links", () => {
    const cleanup = installContextMenuGuard(window);
    const root = document.createElement("div");
    const link = document.createElement("a");
    link.href = "https://example.com";
    link.textContent = "docs";
    const img = document.createElement("img");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    const disabledInput = document.createElement("input");
    disabledInput.type = "text";
    disabledInput.disabled = true;
    root.append(link, img, checkbox, disabledInput);
    document.body.appendChild(root);

    try {
      expect(dispatchContextMenu(root).defaultPrevented).toBe(true);
      expect(dispatchContextMenu(link).defaultPrevented).toBe(true);
      expect(dispatchContextMenu(link.firstChild!).defaultPrevented).toBe(true);
      expect(dispatchContextMenu(img).defaultPrevented).toBe(true);
      expect(dispatchContextMenu(checkbox).defaultPrevented).toBe(true);
      expect(dispatchContextMenu(disabledInput).defaultPrevented).toBe(true);
    } finally {
      cleanup();
      root.remove();
    }
  });

  it("allows the native edit context menu inside enabled text inputs and contenteditable elements", () => {
    const cleanup = installContextMenuGuard(window);
    const root = document.createElement("div");
    const textarea = document.createElement("textarea");
    const textInput = document.createElement("input");
    textInput.type = "text";
    const defaultInput = document.createElement("input");
    const editable = document.createElement("div");
    editable.setAttribute("contenteditable", "true");
    const nestedText = document.createTextNode("hello");
    editable.appendChild(nestedText);
    const nonEditableChild = document.createElement("span");
    nonEditableChild.setAttribute("contenteditable", "false");
    editable.appendChild(nonEditableChild);
    root.append(textarea, textInput, defaultInput, editable);
    document.body.appendChild(root);

    try {
      expect(isEditableTextTarget(textarea)).toBe(true);
      expect(dispatchContextMenu(textarea).defaultPrevented).toBe(false);
      expect(dispatchContextMenu(textInput).defaultPrevented).toBe(false);
      expect(dispatchContextMenu(defaultInput).defaultPrevented).toBe(false);
      expect(dispatchContextMenu(nestedText).defaultPrevented).toBe(false);
      expect(dispatchContextMenu(nonEditableChild).defaultPrevented).toBe(true);
    } finally {
      cleanup();
      root.remove();
    }
  });

  it("lets component-level custom context menus run before the window guard", () => {
    const cleanup = installContextMenuGuard(window);
    const row = document.createElement("div");
    let opened = false;
    row.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      opened = true;
    });
    document.body.appendChild(row);

    try {
      const event = dispatchContextMenu(row);
      expect(opened).toBe(true);
      expect(event.defaultPrevented).toBe(true);
    } finally {
      cleanup();
      row.remove();
    }
  });
});

describe("installExternalLinkHandler", () => {
  afterEach(() => {
    sessionStorage.clear();
    vi.restoreAllMocks();
  });

  it("identifies external http/https/mailto links and skips downloads, hashes, and unsafe schemes", () => {
    const mk = (href: string, opts?: { target?: string; download?: boolean }) => {
      const a = document.createElement("a");
      a.setAttribute("href", href);
      if (opts?.target) a.target = opts.target;
      if (opts?.download) a.setAttribute("download", "file.txt");
      return a;
    };
    const current = "http://127.0.0.1:8087/";

    expect(resolveExternalHref(mk("https://example.com/path?a=1"), current)).toBe("https://example.com/path?a=1");
    expect(resolveExternalHref(mk("http://127.0.0.1:3000/app"), current)).toBe("http://127.0.0.1:3000/app");
    expect(resolveExternalHref(mk("mailto:hi@example.com"), current)).toBe("mailto:hi@example.com");
    expect(resolveExternalHref(mk("/api/v1/spaces/s/attachments/1", { target: "_blank" }), current)).toBe(
      "http://127.0.0.1:8087/api/v1/spaces/s/attachments/1",
    );

    expect(resolveExternalHref(mk("/api/v1/spaces/s/attachments/1", { download: true }), current)).toBeNull();
    expect(resolveExternalHref(mk("https://example.com/file.zip", { download: true }), current)).toBeNull();
    expect(resolveExternalHref(mk("#workspace:docs%2Fplan.md"), current)).toBeNull();
    expect(resolveExternalHref(mk("/spaces/studio"), current)).toBeNull();
    expect(resolveExternalHref(mk("javascript:alert(1)"), current)).toBeNull();
  });

  it("opens external links via the desktop bridge and leaves normal browsers untouched", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const cleanup = installExternalLinkHandler(window);
    const link = document.createElement("a");
    link.href = "https://example.com/docs";
    link.target = "_blank";
    link.innerHTML = "<span>Docs</span>";
    document.body.appendChild(link);

    try {
      // Without a desktop bridge, browser navigation stays native.
      const webClick = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
      link.firstChild!.dispatchEvent(webClick);
      expect(webClick.defaultPrevented).toBe(false);
      expect(fetchSpy).not.toHaveBeenCalled();

      // With a desktop bridge, left-click and middle-click open in the system browser.
      sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:54321");
      sessionStorage.setItem("sumi.desktop.token", "tok-123");

      const desktopClick = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
      link.firstChild!.dispatchEvent(desktopClick);
      expect(desktopClick.defaultPrevented).toBe(true);
      expect(fetchSpy).toHaveBeenCalledWith("http://127.0.0.1:54321/desktop/open-url", {
        method: "POST",
        headers: {
          Authorization: "Bearer tok-123",
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ url: "https://example.com/docs" }),
      });

      const middleClick = new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 });
      link.dispatchEvent(middleClick);
      expect(middleClick.defaultPrevented).toBe(true);
      expect(fetchSpy).toHaveBeenCalledTimes(2);
    } finally {
      cleanup();
      link.remove();
    }
  });

  it("respects already prevented clicks such as workspace file links", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:54321");
    sessionStorage.setItem("sumi.desktop.token", "tok-123");

    const cleanup = installExternalLinkHandler(window);
    const link = document.createElement("a");
    link.href = "https://example.com/intercepted";
    link.addEventListener("click", (e) => e.preventDefault());
    document.body.appendChild(link);

    try {
      const ev = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
      link.dispatchEvent(ev);
      expect(ev.defaultPrevented).toBe(true);
      expect(fetchSpy).not.toHaveBeenCalled();
    } finally {
      cleanup();
      link.remove();
    }
  });
});

describe("window close shortcut", () => {
  afterEach(() => {
    resetLayers();
    sessionStorage.clear();
    vi.restoreAllMocks();
  });

  function enterDesktopShell() {
    sessionStorage.setItem("sumi.shell", "desktop");
    sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:54321");
    sessionStorage.setItem("sumi.desktop.token", "tok-123");
  }

  it("closes the topmost overlay before touching the window", () => {
    enterDesktopShell();
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const close = vi.fn();
    pushLayer(close);
    const cleanup = installWindowCloseHandler(window);

    const event = new KeyboardEvent("keydown", { key: "w", metaKey: true, bubbles: true, cancelable: true });
    window.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(close).toHaveBeenCalledTimes(1);
    expect(fetchSpy).not.toHaveBeenCalled();
    cleanup();
  });

  it("hides the desktop window when no overlay is open", () => {
    enterDesktopShell();
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const cleanup = installWindowCloseHandler(window);

    window.dispatchEvent(new KeyboardEvent("keydown", { key: "W", ctrlKey: true, bubbles: true, cancelable: true }));

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(fetchSpy.mock.calls[0][0]).toBe("http://127.0.0.1:54321/desktop/hide-window");
    cleanup();
  });

  it("ignores the shortcut in the browser shell and leaves other keys alone", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const cleanup = installWindowCloseHandler(window);
    const event = new KeyboardEvent("keydown", { key: "w", metaKey: true, bubbles: true, cancelable: true });
    window.dispatchEvent(event);
    expect(event.defaultPrevented).toBe(false);
    expect(requestWindowClose()).toBe(false);
    expect(fetchSpy).not.toHaveBeenCalled();
    cleanup();
  });

  it("exposes the native menu entry point while installed", () => {
    enterDesktopShell();
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const close = vi.fn();
    pushLayer(close);
    const cleanup = installWindowCloseHandler(window);
    expect(window.__sumiCloseRequest?.()).toBe(true);
    expect(close).toHaveBeenCalledTimes(1);
    cleanup();
    expect(window.__sumiCloseRequest).toBeUndefined();
  });

  it("signals desktop window ready after two animation frames and skips regular browsers", () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const frames: FrameRequestCallback[] = [];
    const rafSpy = vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => {
      frames.push(cb);
      return frames.length;
    });

    // Without bridge coordinates, nothing is scheduled or posted.
    signalDesktopWindowReady(window);
    expect(frames).toHaveLength(0);
    expect(fetchSpy).not.toHaveBeenCalled();

    enterDesktopShell();
    signalDesktopWindowReady(window);
    expect(frames).toHaveLength(1);
    expect(fetchSpy).not.toHaveBeenCalled();

    frames.shift()!(16);
    expect(frames).toHaveLength(1);
    expect(fetchSpy).not.toHaveBeenCalled();

    frames.shift()!(32);
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    expect(fetchSpy).toHaveBeenCalledWith("http://127.0.0.1:54321/desktop/reveal-window", {
      method: "POST",
      headers: { Authorization: "Bearer tok-123" },
    });
    rafSpy.mockRestore();
  });
});
