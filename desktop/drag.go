package main

// desktopWindowDragJS bridges Sumi's CSS `--wails-draggable` titlebar regions to Wails v3's
// native window drag/zoom messages. Because Sumi loads its Web UI from the local/remote HTTP
// Server origin rather than Wails's embedded asset server, `/wails/runtime.js` is not loaded
// and the WebView needs this lightweight listener to invoke `wails:drag` on primary clicks.
const desktopWindowDragJS = `(function () {
  if (window.__sumiDragBridge) return;
  window.__sumiDragBridge = true;

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
})();`
