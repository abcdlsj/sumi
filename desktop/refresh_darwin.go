//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/message.h>
#import <objc/runtime.h>

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

static const CGFloat sumiHeaderHeight = 52.0;
static const CGFloat sumiTrafficLightTop = 16.0;
static const CGFloat sumiTrafficLightLeft = 16.0;

// Places the native traffic-light strip inside Sumi's 52px header without attaching
// an NSToolbar (which inflates window corner radius on macOS 26).
static int sumiAlignTrafficLights(NSWindow *window, CGFloat headerHeight, CGFloat topInset, CGFloat leftInset) {
	if (!window) {
		return 0;
	}
	if (([window styleMask] & NSWindowStyleMaskFullScreen) != 0) {
		return 0;
	}
	NSButton *closeBtn = [window standardWindowButton:NSWindowCloseButton];
	NSButton *miniBtn = [window standardWindowButton:NSWindowMiniaturizeButton];
	NSButton *zoomBtn = [window standardWindowButton:NSWindowZoomButton];
	if (!closeBtn || !miniBtn || !zoomBtn) {
		return 0;
	}
	NSView *titlebarView = [closeBtn superview];
	NSView *container = [titlebarView superview];
	NSView *themeFrame = [container superview];
	if (!titlebarView || !container || !themeFrame) {
		return 0;
	}

	NSRect cFrame = [container frame];
	NSRect tfBounds = [themeFrame bounds];
	NSRect closeFrame = [closeBtn frame];
	if (cFrame.size.height <= 0 || closeFrame.size.height <= 0) {
		return 0;
	}

	// Expand the titlebar container to match Sumi's 52px header while keeping its top edge
	// strictly flush with the window top so _NSTitlebarDecorationView never draws a second
	// offset border line. NSTitlebarView autoresizing keeps hover tracking intact.
	CGFloat targetH = headerHeight;
	CGFloat targetOriginY = tfBounds.size.height - targetH;
	if (fabs(cFrame.origin.y - targetOriginY) > 0.5 || fabs(cFrame.size.height - targetH) > 0.5) {
		cFrame.origin.y = targetOriginY;
		cFrame.size.height = targetH;
		[container setFrame:cFrame];
	}

	NSRect tbBounds = [titlebarView bounds];
	CGFloat targetY = round(tbBounds.size.height - topInset - closeFrame.size.height);
	CGFloat pitch = [miniBtn frame].origin.x - closeFrame.origin.x;
	if (pitch <= 0) {
		pitch = 20.0;
	}
	NSButton *buttons[3] = { closeBtn, miniBtn, zoomBtn };
	for (int i = 0; i < 3; i++) {
		NSRect bf = [buttons[i] frame];
		CGFloat targetX = leftInset + pitch * i;
		if (fabs(bf.origin.x - targetX) > 0.5 || fabs(bf.origin.y - targetY) > 0.5) {
			bf.origin.x = targetX;
			bf.origin.y = targetY;
			[buttons[i] setFrame:bf];
		}
	}
	return 1;
}

@interface SumiTrafficLightKeeper : NSObject {
	NSWindow *_window; // non-owning; associated object is released when window deallocates
	BOOL _applying;
}
- (instancetype)initWithWindow:(NSWindow *)window;
@end

@implementation SumiTrafficLightKeeper
- (instancetype)initWithWindow:(NSWindow *)window {
	self = [super init];
	if (self) {
		_window = window;
		NSNotificationCenter *nc = [NSNotificationCenter defaultCenter];
		NSArray<NSNotificationName> *names = @[
			NSWindowDidResizeNotification,
			NSWindowDidEndLiveResizeNotification,
			NSWindowDidBecomeKeyNotification,
			NSWindowDidResignKeyNotification,
			NSWindowDidExitFullScreenNotification,
		];
		for (NSNotificationName name in names) {
			[nc addObserver:self selector:@selector(realign:) name:name object:window];
		}
		NSView *container = [[[window standardWindowButton:NSWindowCloseButton] superview] superview];
		if (container) {
			[container setPostsFrameChangedNotifications:YES];
			[nc addObserver:self selector:@selector(realign:) name:NSViewFrameDidChangeNotification object:container];
		}
		[self realign:nil];
	}
	return self;
}

- (void)dealloc {
	[[NSNotificationCenter defaultCenter] removeObserver:self];
	[super dealloc];
}

- (void)realign:(NSNotification *)note {
	(void)note;
	if (_applying) {
		return;
	}
	NSWindow *win = _window;
	if (!win) {
		return;
	}
	_applying = YES;
	sumiAlignTrafficLights(win, sumiHeaderHeight, sumiTrafficLightTop, sumiTrafficLightLeft);
	_applying = NO;
}
@end

static char sumiTrafficLightKeeperKey;

static int sumiInstallTrafficLightKeeper(NSWindow *window) {
	if (!window) {
		return 0;
	}
	SumiTrafficLightKeeper *existing = objc_getAssociatedObject(window, &sumiTrafficLightKeeperKey);
	if (existing) {
		[existing realign:nil];
		return 1;
	}
	SumiTrafficLightKeeper *keeper = [[SumiTrafficLightKeeper alloc] initWithWindow:window];
	objc_setAssociatedObject(window, &sumiTrafficLightKeeperKey, keeper, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
	[keeper release];
	return 1;
}

static int sumiConfigureNativeWindowWebView(void *nsWindowPtr, int *refreshUnlockedOut, int *overscrollAppliedOut) {
	if (refreshUnlockedOut) *refreshUnlockedOut = 0;
	if (overscrollAppliedOut) *overscrollAppliedOut = 0;
	if (!nsWindowPtr || ![NSThread isMainThread]) {
		return 0;
	}
	@autoreleasepool {
		NSWindow *window = (__bridge NSWindow *)nsWindowPtr;
		sumiInstallTrafficLightKeeper(window);
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

// Forces macOS to activate Sumi even when a non-activating launcher (Raycast / Spotlight)
// is dismissing its panel and returning focus to the previously active application.
static int sumiActivateWindow(void *nsWindowPtr) {
	if (!nsWindowPtr || ![NSThread isMainThread]) {
		return 0;
	}
	@autoreleasepool {
		NSWindow *window = (__bridge NSWindow *)nsWindowPtr;
		sumiInstallTrafficLightKeeper(window);
		[NSApp activateIgnoringOtherApps:YES];
		[window makeKeyAndOrderFront:nil];
		return 1;
	}
}

// Re-asserts foreground focus only if the window is still visible, so a delayed follow-up
// never resurrects a window that the user immediately hid or closed.
static int sumiActivateWindowIfVisible(void *nsWindowPtr) {
	if (!nsWindowPtr || ![NSThread isMainThread]) {
		return 0;
	}
	@autoreleasepool {
		NSWindow *window = (__bridge NSWindow *)nsWindowPtr;
		if (![window isVisible]) {
			return 0;
		}
		[NSApp activateIgnoringOtherApps:YES];
		[window makeKeyAndOrderFront:nil];
		return 1;
	}
}

static void sumiProbeActivationSelectors(int *appRespondsOut, int *winRespondsOut) {
	*appRespondsOut = [[NSApplication class] instancesRespondToSelector:@selector(activateIgnoringOtherApps:)] ? 1 : 0;
	*winRespondsOut = [[NSWindow class] instancesRespondToSelector:@selector(makeKeyAndOrderFront:)] ? 1 : 0;
}

static void sumiProbeTrafficLightAlignment(double *topFromWindowOut, double *leftOut, double *containerTopGapOut, int *alignedOut) {
	@autoreleasepool {
		NSWindow *w = [[NSWindow alloc]
			initWithContentRect:NSMakeRect(0, 0, 800, 600)
			styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable | NSWindowStyleMaskFullSizeContentView)
			backing:NSBackingStoreBuffered
			defer:NO];
		w.titlebarAppearsTransparent = YES;
		w.titleVisibility = NSWindowTitleHidden;
		[w layoutIfNeeded];
		*alignedOut = sumiAlignTrafficLights(w, sumiHeaderHeight, sumiTrafficLightTop, sumiTrafficLightLeft);
		NSButton *closeBtn = [w standardWindowButton:NSWindowCloseButton];
		NSView *container = [[closeBtn superview] superview];
		NSView *themeFrame = [container superview];
		NSRect cFrame = [container frame];
		NSRect tfBounds = [themeFrame bounds];
		*containerTopGapOut = (double)(tfBounds.size.height - (cFrame.origin.y + cFrame.size.height));
		NSRect btnInWin = [closeBtn convertRect:[closeBtn bounds] toView:nil];
		*topFromWindowOut = (double)(w.frame.size.height - (btnInWin.origin.y + btnInWin.size.height));
		*leftOut = (double)btnInWin.origin.x;
		[w release];
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
	"time"
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

func activateNativeWindow(nativeWindow unsafe.Pointer) bool {
	if nativeWindow == nil {
		return false
	}
	return C.sumiActivateWindow(nativeWindow) != 0
}

func activateNativeWindowIfVisible(nativeWindow unsafe.Pointer) bool {
	if nativeWindow == nil {
		return false
	}
	return C.sumiActivateWindowIfVisible(nativeWindow) != 0
}

func probeActivationSelectors() (appResponds, winResponds bool) {
	var appOK, winOK C.int
	C.sumiProbeActivationSelectors(&appOK, &winOK)
	return appOK != 0, winOK != 0
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

func probeTrafficLightAlignment() (top, left, containerTopGap float64, aligned bool) {
	var t, l, g C.double
	var ok C.int
	C.sumiProbeTrafficLightAlignment(&t, &l, &g, &ok)
	return float64(t), float64(l), float64(g), ok != 0
}

// attachNativeWebViewConfig applies macOS WKWebView settings (120Hz ProMotion refresh and
// overscroll/rubber-band disable) as soon as the main WebviewWindow's native NSWindow exists.
// It returns a synchronous main-thread helper that guarantees configuration has run before reveal.
func attachNativeWebViewConfig(app *application.App, window *application.WebviewWindow, logger *slog.Logger) func() {
	var done atomic.Bool
	applyNow := func() {
		if done.Load() || window == nil {
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
	}
	tryConfigure := func() {
		if done.Load() {
			return
		}
		application.InvokeAsync(applyNow)
	}
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		tryConfigure()
	})
	window.OnWindowEvent(events.Common.WindowShow, func(*application.WindowEvent) {
		tryConfigure()
	})
	return applyNow
}

func showAndActivateWindow(window *application.WebviewWindow, ensureConfigured func(), followUpDelay time.Duration) {
	if window == nil {
		return
	}
	application.InvokeAsync(func() {
		if ensureConfigured != nil {
			ensureConfigured()
		}
		window.UnMinimise()
		window.Show()
		activateNativeWindow(window.NativeWindow())
		window.Focus()
	})
	if followUpDelay > 0 {
		time.AfterFunc(followUpDelay, func() {
			application.InvokeAsync(func() {
				activateNativeWindowIfVisible(window.NativeWindow())
			})
		})
	}
}
