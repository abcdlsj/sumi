package main

// desktopWindowDragJS bridges Sumi's CSS `--wails-draggable` titlebar regions to Wails v3's
// native window drag/zoom messages and suppresses the browser default context menu outside
// editable text fields from document start. Because Sumi loads its Web UI from the HTTP Server
// origin rather than Wails's embedded asset server, `/wails/runtime.js` is not loaded.
const desktopWindowDragJS = `(function () {
  if (window.__sumiDragBridge) return;
  window.__sumiDragBridge = true;

  var editableInputTypes = {
    "": true,
    text: true,
    search: true,
    url: true,
    email: true,
    tel: true,
    password: true,
    number: true,
  };

  function post(msg) {
    try {
      if (window.webkit && window.webkit.messageHandlers && window.webkit.messageHandlers.external) {
        window.webkit.messageHandlers.external.postMessage(msg);
      } else if (window.chrome && window.chrome.webview) {
        window.chrome.webview.postMessage(msg);
      }
    } catch (_) {}
  }

  function isDraggable(event) {
    var node = event.target;
    var el = node && node.nodeType === 1 ? node : node && node.parentElement;
    if (!el || el.isContentEditable) return false;
    var style = window.getComputedStyle(el);
    return !!(style && style.getPropertyValue("--wails-draggable").trim() === "drag");
  }

  function isEditableTarget(node) {
    var el = node && node.nodeType === 1 ? node : node && node.parentElement;
    if (!el || !el.closest) return false;
    var editable = el.closest("textarea, input, [contenteditable]");
    if (!editable) return false;
    var tag = editable.tagName;
    if (tag === "TEXTAREA") return !editable.disabled;
    if (tag === "INPUT") {
      return !editable.disabled && !!editableInputTypes[(editable.type || "").toLowerCase()];
    }
    if (editable.isContentEditable) return true;
    var attr = editable.getAttribute("contenteditable");
    return attr !== null && attr.toLowerCase() !== "false";
  }

  window.addEventListener("mousedown", function (event) {
    if (event.button !== 0 || event.detail !== 1) return;
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    // Leave a narrow perimeter for native macOS edge/corner window resizing.
    if (event.clientY <= 4 || event.clientX <= 4 || event.clientX >= window.innerWidth - 4) return;
    if (!isDraggable(event)) return;
    event.preventDefault();
    post("wails:drag");
  }, true);

  window.addEventListener("dblclick", function (event) {
    if (event.button !== 0 || window !== window.top) return;
    if (!isDraggable(event)) return;
    event.preventDefault();
    event.stopPropagation();
    post("wails:drag:doubleclick");
  }, true);

  window.addEventListener("contextmenu", function (event) {
    if (event.defaultPrevented) return;
    if (isEditableTarget(event.target)) return;
    event.preventDefault();
  }, false);
})();`
