//go:build darwin && cgo

package ui

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include <math.h>
#import <Cocoa/Cocoa.h>
#include <stdio.h>

// No Dock icon and no app menu: a menu-bar accessory.
static void d0m1SetAccessory(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

// A modal Quit / Cancel alert on the main thread; 1 means Quit.
static int d0m1Confirm(const char *question) {
	NSString *q = [NSString stringWithUTF8String:question];
	__block NSModalResponse r = NSAlertSecondButtonReturn;
	void (^ask)(void) = ^{
		[NSApp activateIgnoringOtherApps:YES];
		NSAlert *a = [[NSAlert alloc] init];
		a.messageText = @"AI usage collector";
		a.informativeText = q;
		[a addButtonWithTitle:@"Quit"];
		[a addButtonWithTitle:@"Cancel"];
		r = [a runModal];
	};
	if ([NSThread isMainThread]) {
		ask();
	} else {
		dispatch_sync(dispatch_get_main_queue(), ask);
	}
	return r == NSAlertFirstButtonReturn;
}

static void d0m1OnMain(void (^block)(void)) {
	if ([NSThread isMainThread]) {
		block();
	} else {
		dispatch_sync(dispatch_get_main_queue(), block);
	}
}

// The sheet is one view. The pinned panel and the click popup each host
// one, so the pane, the type and the row colours exist once.
extern void d0m1PanelMoved(int x, int top);
extern void d0m1PanelClosed(void);
extern int d0m1PopupHover(int y);
extern void d0m1PopupClick(int y);
extern void d0m1PopupKey(int key);
extern void d0m1PopupResign(void);
extern void d0m1PopupClosed(void);
extern void d0m1PopupOutside(void);

int d0m1Pad = 12;
int d0m1LineH = 0;
int d0m1RuleH = 9;
int d0m1ActionH = 0;
static NSFont *d0m1Mono;
static NSFont *d0m1LightFont;

static void d0m1Measure(void) {
	if (d0m1LineH > 0) return;
	d0m1Mono = [NSFont monospacedSystemFontOfSize:12 weight:NSFontWeightRegular];
	d0m1LightFont = [NSFont monospacedSystemFontOfSize:12 weight:NSFontWeightLight];
	NSSize sz = [@"Mg" sizeWithAttributes:@{NSFontAttributeName : d0m1Mono}];
	d0m1LineH = (int)ceil(sz.height);
	if (d0m1LineH < 1) d0m1LineH = 14;
	d0m1ActionH = d0m1LineH + 8;
}

static void d0m1Metrics(void) {
	d0m1OnMain(^{ d0m1Measure(); });
}

static int d0m1TextWidth(const char *s) {
	NSString *t = s ? [NSString stringWithUTF8String:s] : @"";
	__block int w = 0;
	d0m1OnMain(^{
		d0m1Measure();
		NSSize sz = [t sizeWithAttributes:@{NSFontAttributeName : d0m1Mono}];
		w = (int)ceil(sz.width);
	});
	return w;
}

static NSColor *d0m1RGB(int hex) {
	return [NSColor colorWithSRGBRed:((hex >> 16) & 255) / 255.0 green:((hex >> 8) & 255) / 255.0 blue:(hex & 255) / 255.0 alpha:1];
}

// Append line[from,to) measured in UTF-16 units (these rows are ASCII plus │).
static void d0m1Append(NSMutableAttributedString *s, NSString *line, NSUInteger from, NSUInteger to, NSFont *font, NSColor *color) {
	if (to <= from || from >= line.length) return;
	to = MIN(to, line.length);
	NSString *bit = [line substringWithRange:NSMakeRange(from, to - from)];
	[s appendAttributedString:[[NSAttributedString alloc] initWithString:bit attributes:@{
		NSFontAttributeName : font, NSForegroundColorAttributeName : color}]];
}

// One row. quiet grays the age, +count, and rolling totals. A LineDim hi span is the
// green "copied". selected is the action-row highlight.
static NSAttributedString *d0m1LineAttr(NSString *body, int kind, int age, int ageEnd, int hi, int hiEnd, int quiet, int selected) {
	d0m1Measure();
	NSColor *muted = d0m1RGB(0x9198a1);
	NSColor *c = d0m1RGB(0xf0f6fc);
	if (kind == 3) c = muted;
	else if (kind == 5) c = selected ? d0m1RGB(0xffffff) : d0m1RGB(0xc9d1d9);
	else if (kind == 6) c = d0m1RGB(0x6e7681);
	else if (kind == 7) c = selected ? d0m1RGB(0xff7b72) : d0m1RGB(0xf85149);
	NSMutableAttributedString *s = [[NSMutableAttributedString alloc] init];
	if ((kind == 1 || kind == 2) && body.length > 0) {
		d0m1Append(s, body, 0, 1, d0m1Mono, d0m1RGB(kind == 1 ? 0x56d364 : 0xf85149));
		d0m1Append(s, body, 1, body.length, d0m1Mono, c);
	} else if (quiet && ageEnd > age && hiEnd > hi && hi >= ageEnd) {
		d0m1Append(s, body, 0, (NSUInteger)age, d0m1Mono, c);
		d0m1Append(s, body, (NSUInteger)age, (NSUInteger)ageEnd, d0m1LightFont, muted);
		d0m1Append(s, body, (NSUInteger)ageEnd, (NSUInteger)hi, d0m1Mono, c);
		d0m1Append(s, body, (NSUInteger)hi, (NSUInteger)hiEnd, d0m1LightFont, muted);
		d0m1Append(s, body, (NSUInteger)hiEnd, body.length, d0m1LightFont, muted);
	} else if (kind == 3 && hiEnd > hi && hi >= 0) {
		d0m1Append(s, body, 0, (NSUInteger)hi, d0m1Mono, c);
		d0m1Append(s, body, (NSUInteger)hi, (NSUInteger)hiEnd, d0m1Mono, d0m1RGB(0x56d364));
		d0m1Append(s, body, (NSUInteger)hiEnd, body.length, d0m1Mono, c);
	} else {
		d0m1Append(s, body, 0, body.length, d0m1Mono, c);
	}
	return s;
}

// Set while the pinned panel is following the pointer, so a refresh does not
// move it.
static BOOL d0m1Dragging;

@interface D0m1SheetView : NSView
@property (copy) NSString *text;
@property (copy) NSString *kinds;
@property (copy) NSString *spans;
@property int pad, lineH, ruleH, actionH;
@property BOOL interactive;
@property NSTrackingArea *track;
@end

@implementation D0m1SheetView
- (BOOL)acceptsFirstResponder { return self.interactive; }
- (int)rowH:(int)kind {
	if (kind == 4) return self.ruleH;
	if (kind >= 5 && kind <= 7) return self.actionH;
	return self.lineH;
}
- (int)yOf:(NSEvent *)e {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	return (int)llround(self.bounds.size.height - p.y);
}
- (void)updateTrackingAreas {
	[super updateTrackingAreas];
	if (self.track) {
		[self removeTrackingArea:self.track];
		self.track = nil;
	}
	if (!self.interactive) return;
	self.track = [[NSTrackingArea alloc] initWithRect:self.bounds
		options:NSTrackingMouseMoved | NSTrackingMouseEnteredAndExited | NSTrackingActiveAlways | NSTrackingInVisibleRect
		owner:self userInfo:nil];
	[self addTrackingArea:self.track];
}
- (void)mouseMoved:(NSEvent *)e {
	if (!self.interactive) return;
	if (d0m1PopupHover([self yOf:e])) [[NSCursor pointingHandCursor] set];
	else [[NSCursor arrowCursor] set];
}
- (void)mouseExited:(NSEvent *)e {
	if (self.interactive) d0m1PopupHover(-1);
	[[NSCursor arrowCursor] set];
}
- (void)mouseDown:(NSEvent *)e {
	// The pinned panel is not interactive. Its view fills the window, so the
	// window's own background-drag never sees the click; track it here. The
	// popup keeps its clicks for the rows.
	if (self.interactive) return;
	NSWindow *win = self.window;
	if (!win) return;
	d0m1Dragging = YES;
	NSPoint startMouse = [NSEvent mouseLocation];
	NSPoint startOrigin = win.frame.origin;
	while (1) {
		NSEvent *next = [win nextEventMatchingMask:(NSEventMaskLeftMouseDragged | NSEventMaskLeftMouseUp)
						  untilDate:[NSDate distantFuture] inMode:NSEventTrackingRunLoopMode dequeue:YES];
		if (!next || next.type == NSEventTypeLeftMouseUp) break;
		NSPoint now = [NSEvent mouseLocation];
		[win setFrameOrigin:NSMakePoint(startOrigin.x + (now.x - startMouse.x), startOrigin.y + (now.y - startMouse.y))];
	}
	d0m1Dragging = NO;
}
- (void)mouseUp:(NSEvent *)e {
	if (self.interactive) d0m1PopupClick([self yOf:e]);
}
- (void)keyDown:(NSEvent *)e {
	if (!self.interactive) { [super keyDown:e]; return; }
	switch (e.keyCode) {
	case 126: d0m1PopupKey(1); break;          // up
	case 125: case 48: d0m1PopupKey(2); break; // down, tab
	case 36: case 76: case 49: d0m1PopupKey(3); break; // return, keypad enter, space
	case 53: d0m1PopupKey(4); break;          // esc
	default: break;
	}
}
- (void)drawRect:(NSRect)dirty {
	NSRect b = self.bounds;
	NSBezierPath *round = [NSBezierPath bezierPathWithRoundedRect:b xRadius:8 yRadius:8];
	[NSGraphicsContext.currentContext saveGraphicsState];
	[round addClip];
	[[d0m1RGB(0x0d1410) colorWithAlphaComponent:0.96] setFill];
	NSRectFill(b);
	NSString *text = self.text ?: @"";
	NSString *kinds = self.kinds ?: @"";
	NSString *spans = self.spans ?: @"";
	if (kinds.length > 0) {
		NSArray<NSString *> *lines = [text componentsSeparatedByString:@"\n"];
		NSArray<NSString *> *spanLines = [spans componentsSeparatedByString:@"\n"];
		CGFloat yTop = self.pad;
		CGFloat H = b.size.height;
		NSUInteger n = MIN(lines.count, (NSUInteger)kinds.length);
		for (NSUInteger i = 0; i < n; i++) {
			int kind = [kinds characterAtIndex:i] - '0';
			int rh = [self rowH:kind];
			CGFloat rowBottom = H - yTop - rh;
			if (kind == 4) {
				[d0m1RGB(0x3d444d) setFill];
				NSRectFill(NSMakeRect(self.pad, rowBottom + rh / 2.0, b.size.width - 2 * self.pad, 1));
			} else {
				int age = 0, ageEnd = 0, hi = 0, hiEnd = 0, quiet = 0, selected = 0;
				if (i < spanLines.count) {
					sscanf(spanLines[i].UTF8String, "%d,%d,%d,%d,%d,%d", &age, &ageEnd, &hi, &hiEnd, &quiet, &selected);
				}
				if (selected) {
					[d0m1RGB(0x17331f) setFill];
					NSRectFill(NSMakeRect(self.pad - 6, rowBottom, b.size.width - 2 * (self.pad - 6), rh));
				}
				NSAttributedString *attr = d0m1LineAttr(lines[i], kind, age, ageEnd, hi, hiEnd, quiet, selected);
				NSSize ts = attr.size;
				[attr drawInRect:NSMakeRect(self.pad, rowBottom + (rh - ts.height) / 2.0, ts.width, ts.height)];
			}
			yTop += rh;
		}
	}
	[NSGraphicsContext.currentContext restoreGraphicsState];
	[d0m1RGB(0x196c2e) setStroke];
	NSBezierPath *edge = [NSBezierPath bezierPathWithRoundedRect:NSInsetRect(b, 0.5, 0.5) xRadius:8 yRadius:8];
	edge.lineWidth = 1;
	[edge stroke];
}
@end

@interface D0m1Closer : NSObject
@property int which; // 0 panel unpin, 1 popup close
@end
@implementation D0m1Closer
- (void)fire:(id)sender {
	if (self.which == 1) d0m1PopupClosed();
	else d0m1PanelClosed();
}
@end

@interface D0m1PanelDelegate : NSObject <NSWindowDelegate>
@end
static NSPanel *d0m1Panel;
static D0m1SheetView *d0m1PanelView;
static NSButton *d0m1PanelClose;
static D0m1Closer *d0m1PanelCloser;
static D0m1PanelDelegate *d0m1PanelDel;
static BOOL d0m1Visible, d0m1Quiet;

@implementation D0m1PanelDelegate
- (void)windowDidMove:(NSNotification *)n {
	if (!d0m1Quiet && d0m1Panel) d0m1PanelMoved((int)d0m1Panel.frame.origin.x, (int)NSMaxY(d0m1Panel.frame));
}
@end

static NSButton *d0m1CloseButton(D0m1Closer *target) {
	NSButton *b = [NSButton buttonWithTitle:@"×" target:target action:@selector(fire:)];
	b.bordered = NO;
	b.contentTintColor = d0m1RGB(0x9198a1);
	return b;
}

static void d0m1Fill(D0m1SheetView *v, NSButton *close, NSString *t, NSString *k, NSString *sp,
	int pad, int lineH, int ruleH, int actionH, int w, int h) {
	v.text = t;
	v.kinds = k;
	v.spans = sp;
	v.pad = pad;
	v.lineH = lineH;
	v.ruleH = ruleH;
	v.actionH = actionH;
	close.frame = NSMakeRect(w - lineH - 6, h - lineH - 6, lineH, lineH);
	[v setNeedsDisplay:YES];
}

static void d0m1Style(NSPanel *p) {
	p.level = NSFloatingWindowLevel;
	p.floatingPanel = YES;
	p.hidesOnDeactivate = NO;
	p.releasedWhenClosed = NO;
	p.opaque = NO;
	p.hasShadow = YES;
	p.backgroundColor = NSColor.clearColor;
	p.animationBehavior = NSWindowAnimationBehaviorNone;
}

// The pinned panel. x, top is the saved top-left (y up) when placed.
static void d0m1PanelShow(const char *text, const char *kinds, const char *spans,
	int pad, int lineH, int ruleH, int actionH, int w, int h, int x, int top, int placed) {
	NSString *t = [NSString stringWithUTF8String:text ? text : ""];
	NSString *k = [NSString stringWithUTF8String:kinds ? kinds : ""];
	NSString *sp = [NSString stringWithUTF8String:spans ? spans : ""];
	d0m1OnMain(^{
		if (!d0m1Panel) {
			d0m1Panel = [[NSPanel alloc] initWithContentRect:NSMakeRect(0, 0, 200, 80)
				styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
				backing:NSBackingStoreBuffered defer:NO];
			d0m1Style(d0m1Panel);
			d0m1Panel.movableByWindowBackground = YES;
			d0m1Panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorStationary |
				NSWindowCollectionBehaviorIgnoresCycle | NSWindowCollectionBehaviorFullScreenAuxiliary;
			d0m1PanelView = [[D0m1SheetView alloc] initWithFrame:NSMakeRect(0, 0, 200, 80)];
			d0m1PanelView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
			d0m1Panel.contentView = d0m1PanelView;
			d0m1PanelCloser = [[D0m1Closer alloc] init];
			d0m1PanelCloser.which = 0;
			d0m1PanelClose = d0m1CloseButton(d0m1PanelCloser);
			[d0m1PanelView addSubview:d0m1PanelClose];
			d0m1PanelDel = [[D0m1PanelDelegate alloc] init];
			d0m1Panel.delegate = d0m1PanelDel;
		}
		d0m1Fill(d0m1PanelView, d0m1PanelClose, t, k, sp, pad, lineH, ruleH, actionH, w, h);
		// A content refresh must not pull the panel out from under the pointer.
		if (d0m1Dragging) return;
		NSRect f = d0m1Panel.frame;
		CGFloat left = f.origin.x, topY = NSMaxY(f);
		if (!d0m1Visible) {
			NSRect vf = NSScreen.mainScreen.visibleFrame;
			left = placed ? x : NSMaxX(vf) - w - 16;
			topY = placed ? top : NSMaxY(vf) - 16;
		}
		d0m1Quiet = YES;
		[d0m1Panel setFrame:NSMakeRect(left, topY - h, w, h) display:YES];
		d0m1Quiet = NO;
		if (!d0m1Visible) [d0m1Panel orderFrontRegardless];
		d0m1Visible = YES;
	});
}

static void d0m1PanelHide(void) {
	d0m1OnMain(^{
		[d0m1Panel orderOut:nil];
		d0m1Visible = NO;
	});
}

// The click popup: the same sheet, key, so it takes the keyboard and closes
// when focus leaves. Its top-left in y-up points is (x, y + h) when place.
@interface D0m1KeyPanel : NSPanel
@end
@implementation D0m1KeyPanel
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }
@end

@interface D0m1PopupDelegate : NSObject <NSWindowDelegate>
@end
static D0m1KeyPanel *d0m1Popup;
static D0m1SheetView *d0m1PopupView;
static NSButton *d0m1PopupClose;
static D0m1Closer *d0m1PopupCloser;
static D0m1PopupDelegate *d0m1PopupDel;
static BOOL d0m1PopupVisible, d0m1PopupReady;
static id d0m1OutsideLocal;

@implementation D0m1PopupDelegate
- (void)windowDidResignKey:(NSNotification *)n {
	if (d0m1PopupReady) d0m1PopupResign();
}
@end

static void d0m1WatchOutside(void) {
	if (d0m1OutsideLocal) return;
	// Defer the close: removing a monitor from inside its own handler is unsafe.
	NSEventMask mask = NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown;
	d0m1OutsideLocal = [NSEvent addLocalMonitorForEventsMatchingMask:mask
		handler:^NSEvent *(NSEvent *e) {
			if (d0m1Popup && e.window != d0m1Popup) {
				dispatch_async(dispatch_get_main_queue(), ^{ d0m1PopupOutside(); });
			}
			return e;
		}];
	// A click in another app resigns key (windowDidResignKey). A global
	// monitor is not used: it can raise an accessibility prompt.
}

static void d0m1PopupShow(const char *text, const char *kinds, const char *spans,
	int pad, int lineH, int ruleH, int actionH, int w, int h, int x, int y, int place) {
	NSString *t = [NSString stringWithUTF8String:text ? text : ""];
	NSString *k = [NSString stringWithUTF8String:kinds ? kinds : ""];
	NSString *sp = [NSString stringWithUTF8String:spans ? spans : ""];
	d0m1OnMain(^{
		if (!d0m1Popup) {
			d0m1Popup = [[D0m1KeyPanel alloc] initWithContentRect:NSMakeRect(0, 0, 200, 80)
				styleMask:NSWindowStyleMaskBorderless
				backing:NSBackingStoreBuffered defer:NO];
			d0m1Style(d0m1Popup);
			d0m1Popup.level = NSPopUpMenuWindowLevel;
			d0m1Popup.acceptsMouseMovedEvents = YES;
			d0m1Popup.movableByWindowBackground = NO;
			d0m1Popup.collectionBehavior = NSWindowCollectionBehaviorIgnoresCycle | NSWindowCollectionBehaviorFullScreenAuxiliary;
			d0m1PopupView = [[D0m1SheetView alloc] initWithFrame:NSMakeRect(0, 0, 200, 80)];
			d0m1PopupView.interactive = YES;
			d0m1PopupView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
			d0m1Popup.contentView = d0m1PopupView;
			d0m1PopupCloser = [[D0m1Closer alloc] init];
			d0m1PopupCloser.which = 1;
			d0m1PopupClose = d0m1CloseButton(d0m1PopupCloser);
			[d0m1PopupView addSubview:d0m1PopupClose];
			d0m1PopupDel = [[D0m1PopupDelegate alloc] init];
			d0m1Popup.delegate = d0m1PopupDel;
		}
		d0m1Fill(d0m1PopupView, d0m1PopupClose, t, k, sp, pad, lineH, ruleH, actionH, w, h);
		if (place || !d0m1PopupVisible) {
			d0m1PopupReady = NO;
			[d0m1Popup setFrame:NSMakeRect(x, y, w, h) display:YES];
		} else {
			NSRect f = d0m1Popup.frame;
			[d0m1Popup setFrame:NSMakeRect(f.origin.x, f.origin.y, w, h) display:YES];
		}
		if (!d0m1PopupVisible) {
			d0m1PopupReady = NO;
			[NSApp activateIgnoringOtherApps:YES];
			[d0m1Popup makeKeyAndOrderFront:nil];
			[d0m1Popup makeFirstResponder:d0m1PopupView];
			d0m1WatchOutside();
			d0m1PopupVisible = YES;
			d0m1PopupReady = YES;
		}
	});
}

static void d0m1PopupHide(void) {
	d0m1OnMain(^{
		d0m1PopupReady = NO;
		d0m1PopupVisible = NO;
		[d0m1Popup orderOut:nil];
		if (d0m1OutsideLocal) {
			[NSEvent removeMonitor:d0m1OutsideLocal];
			d0m1OutsideLocal = nil;
		}
		[[NSCursor arrowCursor] set];
	});
}

// The menu-bar item's frame in y-up points, or the cursor when it has none.
static void d0m1StatusAnchor(int *x, int *y, int *w, int *h, int *mouse) {
	__block NSRect f = NSZeroRect;
	__block int byMouse = 1;
	d0m1OnMain(^{
		for (NSWindow *win in NSApp.windows) {
			if ([NSStringFromClass(win.class) rangeOfString:@"StatusBar"].location == NSNotFound) continue;
			NSRect r = win.frame;
			// The icon's window is a short rectangle up in the menu bar.
			// A zero-height or full-width window is not it.
			if (r.size.width < 8 || r.size.width > 80 || r.size.height < 8 || r.size.height > 40) continue;
			BOOL upTop = NO;
			for (NSScreen *s in NSScreen.screens) {
				if (NSMaxY(r) >= NSMaxY(s.frame) - 8 && NSMinY(r) <= NSMaxY(s.frame)) upTop = YES;
			}
			if (!upTop) continue;
			f = r;
			byMouse = 0;
			break;
		}
		if (byMouse) {
			// The click is on the icon, so the pointer is the anchor.
			NSPoint p = NSEvent.mouseLocation;
			f = NSMakeRect(p.x - 4, p.y - 4, 8, 8);
		}
	});
	*x = (int)llround(f.origin.x);
	*y = (int)llround(f.origin.y);
	*w = (int)llround(f.size.width);
	*h = (int)llround(f.size.height);
	*mouse = byMouse;
}

// The screen under ax, ay (y up): its frame, its visible frame, and the
// primary screen's top, which is y-down 0.
static void d0m1ScreenAt(int ax, int ay, int *mx, int *my, int *mw, int *mh,
	int *vx, int *vy, int *vw, int *vh, int *flip) {
	__block NSRect frame = NSZeroRect, visible = NSZeroRect;
	__block int top = 0;
	d0m1OnMain(^{
		NSPoint pt = NSMakePoint(ax, ay);
		NSScreen *screen = NSScreen.mainScreen;
		for (NSScreen *s in NSScreen.screens) {
			if (NSPointInRect(pt, s.frame)) { screen = s; break; }
		}
		if (!screen) screen = NSScreen.mainScreen;
		frame = screen.frame;
		visible = screen.visibleFrame;
		top = (int)llround(NSMaxY(NSScreen.screens[0].frame));
	});
	*mx = (int)llround(frame.origin.x);
	*my = (int)llround(frame.origin.y);
	*mw = (int)llround(frame.size.width);
	*mh = (int)llround(frame.size.height);
	*vx = (int)llround(visible.origin.x);
	*vy = (int)llround(visible.origin.y);
	*vw = (int)llround(visible.size.width);
	*vh = (int)llround(visible.size.height);
	*flip = top;
}
*/
import "C"

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unsafe"

	"github.com/7-of-9/tokenmaxr/collector/internal/tray"
)

// setAccessory runs on the main thread (systray locks it at init).
func setAccessory() { C.d0m1SetAccessory() }

// confirm is a native NSAlert (called from a click goroutine; the alert
// itself runs on the main thread).
func confirm(question string) bool {
	q := C.CString(question)
	defer C.free(unsafe.Pointer(q))
	return C.d0m1Confirm(q) == 1
}

func sheetMetrics() tray.Metrics {
	C.d0m1Metrics()
	return tray.Metrics{Pad: int(C.d0m1Pad), LineH: int(C.d0m1LineH), RuleH: int(C.d0m1RuleH), ActionH: int(C.d0m1ActionH)}
}

func textWidth(s string) int {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return int(C.d0m1TextWidth(cs))
}

// encodeLines is the sheet's wire form: kinds one digit a line, spans
// "age,ageEnd,hi,hiEnd,quiet,selected".
func encodeLines(lines []tray.PanelLine) (text, kinds, spans string) {
	var tb, kb, sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			tb.WriteByte('\n')
			sb.WriteByte('\n')
		}
		tb.WriteString(l.Text)
		kb.WriteString(strconv.Itoa(int(l.Kind)))
		quiet, sel := 0, 0
		if l.Quiet {
			quiet = 1
		}
		if l.Selected {
			sel = 1
		}
		fmt.Fprintf(&sb, "%d,%d,%d,%d,%d,%d", l.Age, l.AgeEnd, l.Hi, l.HiEnd, quiet, sel)
	}
	return tb.String(), kb.String(), sb.String()
}

// sheetBox is the sheet's size. wider only stretches the width (the popup's
// "copied" and armed-Quit lines), so showing them does not resize it.
func sheetBox(lines, wider []tray.PanelLine) (text, kinds, spans string, m tray.Metrics, w, h int) {
	m = sheetMetrics()
	text, kinds, spans = encodeLines(lines)
	maxW := 0
	for _, set := range [][]tray.PanelLine{lines, wider} {
		for i, l := range set {
			if l.Kind == tray.LineRule {
				continue
			}
			tw := textWidth(l.Text)
			if i == 0 {
				tw += 10 + m.LineH // the × beside the status line
			}
			if tw > maxW {
				maxW = tw
			}
		}
	}
	return text, kinds, spans, m, maxW + 2*m.Pad, m.Height(lines)
}

func cstr(s string) *C.char {
	return C.CString(s)
}

var (
	panelMu    sync.Mutex
	panelH     Handler
	panelShown bool
	panelLast  []tray.PanelLine
)

// setPanel shows, updates or hides the pinned panel (renderer.SetPanel).
func setPanel(s tray.PanelState, h Handler) {
	panelMu.Lock()
	panelH = h
	if !s.Shown {
		shown := panelShown
		panelShown, panelLast = false, nil
		panelMu.Unlock()
		if shown {
			C.d0m1PanelHide()
		}
		return
	}
	if panelShown && slices.Equal(panelLast, s.Lines) {
		panelMu.Unlock()
		return
	}
	lines := slices.Clone(s.Lines)
	x, y, placed := s.X, s.Y, s.Placed
	panelShown, panelLast = true, lines
	panelMu.Unlock()

	text, kinds, spans, m, w, ht := sheetBox(lines, lines)
	ct, ck, cs := cstr(text), cstr(kinds), cstr(spans)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cs))
	p := 0
	if placed {
		p = 1
	}
	C.d0m1PanelShow(ct, ck, cs, C.int(m.Pad), C.int(m.LineH), C.int(m.RuleH), C.int(m.ActionH),
		C.int(w), C.int(ht), C.int(x), C.int(y), C.int(p))
}

// closePanel hides the panel (the app quits).
func closePanel() { setPanel(tray.PanelState{}, panelHandler()) }

func panelHandler() Handler {
	panelMu.Lock()
	defer panelMu.Unlock()
	return panelH
}

func showPopupWindow(text, kinds, spans string, m tray.Metrics, w, h, x, y int, place bool) {
	ct, ck, cs := cstr(text), cstr(kinds), cstr(spans)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cs))
	pl := 0
	if place {
		pl = 1
	}
	C.d0m1PopupShow(ct, ck, cs, C.int(m.Pad), C.int(m.LineH), C.int(m.RuleH), C.int(m.ActionH),
		C.int(w), C.int(h), C.int(x), C.int(y), C.int(pl))
}

func hidePopupWindow() { C.d0m1PopupHide() }

func statusAnchor() (x, y, w, h int, by string) {
	var ax, ay, aw, ah, mouse C.int
	C.d0m1StatusAnchor(&ax, &ay, &aw, &ah, &mouse)
	by = "icon"
	if mouse != 0 {
		by = "cursor"
	}
	return int(ax), int(ay), int(aw), int(ah), by
}

// screenAt returns the monitor and the work area under a y-up point, both
// y-down, plus the flip (primary top).
func screenAt(x, y int) (mon, work tray.Rect, flip int) {
	var mx, my, mw, mh, vx, vy, vw, vh, fl C.int
	C.d0m1ScreenAt(C.int(x), C.int(y), &mx, &my, &mw, &mh, &vx, &vy, &vw, &vh, &fl)
	flip = int(fl)
	mon = tray.FlipDown(int(mx), int(my), int(mw), int(mh), flip)
	work = tray.FlipDown(int(vx), int(vy), int(vw), int(vh), flip)
	return mon, work, flip
}
