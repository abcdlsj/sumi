//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/message.h>

static WKWebView *sumiFindWebViewInView(NSView *view) {
	if (!view) {
		return nil;
	}
	if ([view isKindOfClass:[WKWebView class]]) {
		return (WKWebView *)view;
	}
	for (NSView *sub in [view subviews]) {
		WKWebView *found = sumiFindWebViewInView(sub);
		if (found) {
			return found;
		}
	}
	return nil;
}

static WKWebView *sumiFindWebView(NSWindow *window) {
	if (!window) {
		return nil;
	}
	SEL webViewSel = NSSelectorFromString(@"webView");
	if ([window respondsToSelector:webViewSel]) {
		id candidate = ((id (*)(id, SEL))objc_msgSend)(window, webViewSel);
		if ([candidate isKindOfClass:[WKWebView class]]) {
			return (WKWebView *)candidate;
		}
	}
	return sumiFindWebViewInView([window contentView]);
}

// Disables WebKit's default 60fps cap on ProMotion displays so scrolling and animations
// run at native 120Hz. Uses runtime selector checks so missing SPI on future macOS versions
// degrades cleanly to a no-op.
static int sumiDisable60FPSCapOnPreferences(WKPreferences *prefs) {
	if (!prefs) {
		return 0;
	}
	NSString *targetKey = @"PreferPageRenderingUpdatesNear60FPSEnabled";
	SEL keySel = NSSelectorFromString(@"key");

	struct {
		NSString *listSelName;
		NSString *setSelName;
	} variants[2] = {
		{ @"_features", @"_setEnabled:forFeature:" },
		{ @"_experimentalFeatures", @"_setEnabled:forExperimentalFeature:" },
	};

	Class prefsClass = [WKPreferences class];
	for (int i = 0; i < 2; i++) {
		SEL listSel = NSSelectorFromString(variants[i].listSelName);
		SEL setSel = NSSelectorFromString(variants[i].setSelName);
		if (![prefsClass respondsToSelector:listSel] || ![prefs respondsToSelector:setSel]) {
			continue;
		}
		NSArray *features = ((id (*)(id, SEL))objc_msgSend)(prefsClass, listSel);
		if (![features isKindOfClass:[NSArray class]]) {
			continue;
		}
		for (id feature in features) {
			if (![feature respondsToSelector:keySel]) {
				continue;
			}
			id key = ((id (*)(id, SEL))objc_msgSend)(feature, keySel);
			if ([key isKindOfClass:[NSString class]] && [(NSString *)key isEqualToString:targetKey]) {
				((void (*)(id, SEL, BOOL, id))objc_msgSend)(prefs, setSel, NO, feature);
				return 1;
			}
		}
	}
	return 0;
}

static int sumiUnlockWindowHighRefreshRate(void *nsWindowPtr) {
	if (!nsWindowPtr || ![NSThread isMainThread]) {
		return 0;
	}
	@autoreleasepool {
		NSWindow *window = (__bridge NSWindow *)nsWindowPtr;
		WKWebView *webView = sumiFindWebView(window);
		if (!webView) {
			return 0;
		}
		return sumiDisable60FPSCapOnPreferences(webView.configuration.preferences);
	}
}

static int sumiRead60FPSCapOnPreferences(WKPreferences *prefs) {
	if (!prefs) {
		return -1;
	}
	NSString *targetKey = @"PreferPageRenderingUpdatesNear60FPSEnabled";
	SEL listSel = NSSelectorFromString(@"_features");
	SEL isSel = NSSelectorFromString(@"_isEnabledForFeature:");
	SEL keySel = NSSelectorFromString(@"key");
	Class prefsClass = [WKPreferences class];
	if (![prefsClass respondsToSelector:listSel] || ![prefs respondsToSelector:isSel]) {
		return -1;
	}
	NSArray *features = ((id (*)(id, SEL))objc_msgSend)(prefsClass, listSel);
	if (![features isKindOfClass:[NSArray class]]) {
		return -1;
	}
	for (id feature in features) {
		if (![feature respondsToSelector:keySel]) {
			continue;
		}
		id key = ((id (*)(id, SEL))objc_msgSend)(feature, keySel);
		if ([key isKindOfClass:[NSString class]] && [(NSString *)key isEqualToString:targetKey]) {
			return ((BOOL (*)(id, SEL, id))objc_msgSend)(prefs, isSel, feature) ? 1 : 0;
		}
	}
	return -1;
}

static void sumiProbe60FPSCapToggle(int *beforeOut, int *afterOut, int *okOut) {
	@autoreleasepool {
		WKPreferences *prefs = [[WKPreferences alloc] init];
		*beforeOut = sumiRead60FPSCapOnPreferences(prefs);
		*okOut = sumiDisable60FPSCapOnPreferences(prefs);
		*afterOut = sumiRead60FPSCapOnPreferences(prefs);
	}
}
*/
import "C"

import (
	"log/slog"
	"sync/atomic"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func unlockNativeWindowRefreshRate(nativeWindow unsafe.Pointer) bool {
	if nativeWindow == nil {
		return false
	}
	return C.sumiUnlockWindowHighRefreshRate(nativeWindow) != 0
}

func probeHighRefreshRateToggle() (before, after int, ok bool) {
	var b, a, o C.int
	C.sumiProbe60FPSCapToggle(&b, &a, &o)
	return int(b), int(a), o != 0
}

// attachHighRefreshRate disables WebKit's 60fps cap on the main WebviewWindow as soon as its
// native NSWindow/WKWebView exists.
func attachHighRefreshRate(app *application.App, window *application.WebviewWindow, logger *slog.Logger) {
	var done atomic.Bool
	tryUnlock := func() {
		if done.Load() {
			return
		}
		application.InvokeAsync(func() {
			if done.Load() {
				return
			}
			if unlockNativeWindowRefreshRate(window.NativeWindow()) {
				done.Store(true)
				logger.Info("Unlocked native WKWebView high refresh rate (120Hz)")
			}
		})
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		tryUnlock()
	})
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		tryUnlock()
	})
}
