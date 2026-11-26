import { describe, expect, it } from "vitest";
import { installContextMenuGuard, isEditableTextTarget } from "./shell";

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
