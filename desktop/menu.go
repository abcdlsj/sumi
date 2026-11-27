package main

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// closeRequestJS asks the web shell to close its topmost overlay, falling back to
// hiding the window when nothing is open.
const closeRequestJS = `(() => { const request = window.__sumiCloseRequest; if (typeof request === "function") { request(); return; } window.dispatchEvent(new CustomEvent("sumi:close-window")); })()`

// installApplicationMenu keeps the standard macOS menu bar but hands File > Close
// to the web shell instead of closing the window: ⌘W belongs to the web app so it
// can dismiss a lightbox, preview or dialog before the window hides.
func installApplicationMenu(app *application.App, window *application.WebviewWindow) {
	menu := application.DefaultApplicationMenu()
	closeItem := menu.FindByRole(application.CloseWindow)
	if closeItem != nil {
		// A native key equivalent would swallow ⌘W before the WebView sees it, so the
		// accelerator moves to the web shell and the menu item only reports the click.
		closeItem.SetRole(application.NoRole)
		closeItem.RemoveAccelerator()
		closeItem.SetLabel("Close Window")
		closeItem.OnClick(func(*application.Context) {
			window.ExecJS(closeRequestJS)
		})
	}
	app.Menu.SetApplicationMenu(menu)
}
