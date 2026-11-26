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

// Disables macOS WKWebView rubber-banding/bounce and browser swipe/zoom gestures so
// pulling past viewport bounds never exposes a blank under-page background.
static int sumiDisableWebViewOverscroll(WKWebView *webView) {
	if (!webView) {
		return 0;
	}
	webView.allowsBackForwardNavigationGestures = NO;
	webView.allowsMagnification = NO;

	int applied = 0;
	NSString *selectors[3] = {
		@"_setRubberBandingEnabled:",
		@"_setAlwaysBounceVertical:",
		@"_setAlwaysBounceHorizontal:",
	};
	for (int i = 0; i < 3; i++) {
		SEL sel = NSSelectorFromString(selectors[i]);
		if ([webView respondsToSelector:sel]) {
			((void (*)(id, SEL, BOOL))objc_msgSend)(webView, sel, NO);
			applied++;
		}
	}
	return applied;
}

static int sumiReadBoolSelector(id obj, NSString *selName) {
	if (!obj) {
		return -1;
	}
	SEL sel = NSSelectorFromString(selName);
	if (![obj respondsToSelector:sel]) {
		return -1;
	}
	return ((BOOL (*)(id, SEL))objc_msgSend)(obj, sel) ? 1 : 0;
}

static int sumiConfigureNativeWindowWebView(void *nsWindowPtr, int *refreshUnlockedOut, int *overscrollAppliedOut) {
	if (refreshUnlockedOut) *refreshUnlockedOut = 0;
	if (overscrollAppliedOut) *overscrollAppliedOut = 0;
	if (!nsWindowPtr || ![NSThread isMainThread]) {
		return 0;
	}
	@autoreleasepool {
		NSWindow *window = (__bridge NSWindow *)nsWindowPtr;
		WKWebView *webView = sumiFindWebView(window);
		if (!webView) {
			return 0;
		}
		int unlocked = sumiDisable60FPSCapOnPreferences(webView.configuration.preferences);
		int overscroll = sumiDisableWebViewOverscroll(webView);
		if (refreshUnlockedOut) *refreshUnlockedOut = unlocked;
		if (overscrollAppliedOut) *overscrollAppliedOut = overscroll;
		return 1;
	}
}

static void sumiProbeWebViewOverscrollDisable(
	int *rbBefore, int *rbAfter,
	int *bvBefore, int *bvAfter,
	int *bhBefore, int *bhAfter,
	int *appliedOut
) {
	@autoreleasepool {
		WKWebView *webView = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, 320, 240)];
		*rbBefore = sumiReadBoolSelector(webView, @"_rubberBandingEnabled");
		*bvBefore = sumiReadBoolSelector(webView, @"_alwaysBounceVertical");
		*bhBefore = sumiReadBoolSelector(webView, @"_alwaysBounceHorizontal");
		*appliedOut = sumiDisableWebViewOverscroll(webView);
		*rbAfter = sumiReadBoolSelector(webView, @"_rubberBandingEnabled");
		*bvAfter = sumiReadBoolSelector(webView, @"_alwaysBounceVertical");
		*bhAfter = sumiReadBoolSelector(webView, @"_alwaysBounceHorizontal");
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

func configureNativeWindowWebView(nativeWindow unsafe.Pointer) (found, refreshUnlocked bool, overscrollApplied int) {
	if nativeWindow == nil {
		return false, false, 0
	}
	var unlocked, overscroll C.int
	ok := C.sumiConfigureNativeWindowWebView(nativeWindow, &unlocked, &overscroll)
	return ok != 0, unlocked != 0, int(overscroll)
}

func probeHighRefreshRateToggle() (before, after int, ok bool) {
	var b, a, o C.int
	C.sumiProbe60FPSCapToggle(&b, &a, &o)
	return int(b), int(a), o != 0
}

type overscrollProbeResult struct {
	rubberBandBefore, rubberBandAfter int
	bounceVBefore, bounceVAfter       int
	bounceHBefore, bounceHAfter       int
	applied                           int
}

func probeWebViewOverscrollDisable() overscrollProbeResult {
	var rbB, rbA, bvB, bvA, bhB, bhA, applied C.int
	C.sumiProbeWebViewOverscrollDisable(&rbB, &rbA, &bvB, &bvA, &bhB, &bhA, &applied)
	return overscrollProbeResult{
		rubberBandBefore: int(rbB),
		rubberBandAfter:  int(rbA),
		bounceVBefore:    int(bvB),
		bounceVAfter:     int(bvA),
		bounceHBefore:    int(bhB),
		bounceHAfter:     int(bhA),
		applied:          int(applied),
	}
}

// attachNativeWebViewConfig applies macOS WKWebView settings (120Hz ProMotion refresh and
// overscroll/rubber-band disable) as soon as the main WebviewWindow's native NSWindow exists.
func attachNativeWebViewConfig(app *application.App, window *application.WebviewWindow, logger *slog.Logger) {
	var done atomic.Bool
	tryConfigure := func() {
		if done.Load() {
			return
		}
		application.InvokeAsync(func() {
			if done.Load() {
				return
			}
			found, refreshUnlocked, overscrollApplied := configureNativeWindowWebView(window.NativeWindow())
			if !found {
				return
			}
			done.Store(true)
			logger.Info(
				"Configured native WKWebView",
				"high_refresh_unlocked", refreshUnlocked,
				"overscroll_flags_disabled", overscrollApplied,
			)
		})
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		tryConfigure()
	})
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		tryConfigure()
	})
}
