//go:build darwin && cgo

package ui

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include <math.h>
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>
#include <stdio.h>

// Tray only: no Dock icon and no app menu, a menu-bar accessory.
static void d0m1SetAccessory(void) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}

extern void d0m1WindowReopen(void);
extern int d0m1WindowQuit(void);
static void d0m1ObserveHide(void);

// The app menu's Quit (Cmd-Q) quits as the Quit row does.
@interface D0m1AppActions : NSObject
- (void)quitApp:(id)sender;
@end
@implementation D0m1AppActions
- (void)quitApp:(id)sender { (void)d0m1WindowQuit(); }
@end
static D0m1AppActions *d0m1Actions;

// A click on the Dock icon (the app "reopened") shows the main window.
static BOOL d0m1ShouldReopen(id self, SEL _cmd, NSApplication *app, BOOL visible) {
	d0m1WindowReopen();
	return NO;
}

// The Dock menu's Quit, and a logout, restart or shutdown, ask the app to
// terminate (the quit Apple event: NSApp terminate:), which would end the
// process without the app's own quit. So the termination is held
// (NSTerminateLater) while the app quits as Cmd-Q does (stop collecting,
// mark it stopped), and its quit answers it (d0m1ReplyTerminate) instead
// of stopping the event loop; after 10 s it is answered anyway, so a logout
// never waits on the app. Before the app is up, it terminates at once.
static BOOL d0m1Terminating;

static void d0m1EndTerminate(void) {
	if (!d0m1Terminating) return;
	d0m1Terminating = NO;
	[NSApp replyToApplicationShouldTerminate:YES];
}

static NSApplicationTerminateReply d0m1ShouldTerminate(id self, SEL _cmd, NSApplication *app) {
	if (d0m1Terminating) return NSTerminateLater;
	d0m1Terminating = YES;
	if (!d0m1WindowQuit()) {
		d0m1Terminating = NO;
		return NSTerminateNow;
	}
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 10 * (int64_t)NSEC_PER_SEC), dispatch_get_main_queue(), ^{ d0m1EndTerminate(); });
	return NSTerminateLater;
}

// Window mode: a regular app with a Dock icon (applicationIconImage, set by
// d0m1SetDockIcon) and an app menu (Hide, Quit with Cmd-Q; Minimize, Close
// with Cmd-M, Cmd-W). It runs on the main thread before systray starts.
static void d0m1SetRegular(const char *name) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	NSString *title = [NSString stringWithUTF8String:name ? name : ""];
	d0m1Actions = [[D0m1AppActions alloc] init];
	NSMenu *bar = [[NSMenu alloc] init];
	NSMenuItem *appItem = [[NSMenuItem alloc] init];
	[bar addItem:appItem];
	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:title];
	[appMenu addItemWithTitle:[@"Hide " stringByAppendingString:title] action:@selector(hide:) keyEquivalent:@"h"];
	[appMenu addItem:[NSMenuItem separatorItem]];
	NSMenuItem *quit = [appMenu addItemWithTitle:[@"Quit " stringByAppendingString:title] action:@selector(quitApp:) keyEquivalent:@"q"];
	quit.target = d0m1Actions;
	appItem.submenu = appMenu;
	NSMenuItem *winItem = [[NSMenuItem alloc] init];
	[bar addItem:winItem];
	NSMenu *winMenu = [[NSMenu alloc] initWithTitle:@"Window"];
	[winMenu addItemWithTitle:@"Minimize" action:@selector(performMiniaturize:) keyEquivalent:@"m"];
	[winMenu addItemWithTitle:@"Close" action:@selector(performClose:) keyEquivalent:@"w"];
	winItem.submenu = winMenu;
	NSApp.mainMenu = bar;
	NSApp.windowsMenu = winMenu;
	// fyne.io/systray (v1.12) makes its SystrayAppDelegate the app's
	// delegate; the Dock's reopen and a request to terminate are answered
	// by adding the delegate methods to that class before systray sets it
	// (it implements neither).
	Class c = objc_lookUpClass("SystrayAppDelegate");
	if (c) {
		char types[16];
		snprintf(types, sizeof types, "%s@:@%s", @encode(BOOL), @encode(BOOL));
		class_addMethod(c, @selector(applicationShouldHandleReopen:hasVisibleWindows:), (IMP)d0m1ShouldReopen, types);
		char quitTypes[16];
		snprintf(quitTypes, sizeof quitTypes, "%s@:@", @encode(NSApplicationTerminateReply));
		class_addMethod(c, @selector(applicationShouldTerminate:), (IMP)d0m1ShouldTerminate, quitTypes);
	}
	d0m1ObserveHide();
}

// The Dock icon: the status dot (PNG bytes, copied before returning).
static void d0m1SetDockIcon(const void *png, int n) {
	if (!png || n <= 0) return;
	NSData *d = [NSData dataWithBytes:png length:(NSUInteger)n];
	void (^set)(void) = ^{
		NSImage *img = [[NSImage alloc] initWithData:d];
		if (img) NSApp.applicationIconImage = img;
	};
	if ([NSThread isMainThread]) set();
	else dispatch_async(dispatch_get_main_queue(), set);
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

// d0m1ReplyTerminate ends a held termination (the app's quit is done): 1
// when there was one, so the app exits through it rather than by stopping
// the event loop.
static int d0m1ReplyTerminate(void) {
	__block int held = 0;
	d0m1OnMain(^{
		held = d0m1Terminating;
		if (held) dispatch_async(dispatch_get_main_queue(), ^{ d0m1EndTerminate(); });
	});
	return held;
}

// The sheet is one view. The pinned panel and the click popup each host
// one, so the pane, the type and the row colours exist once.
extern void d0m1PanelMoved(int x, int top);
extern void d0m1PanelClosed(void);
extern int d0m1PopupHover(int x, int y);
extern void d0m1PopupClick(int x, int y);
extern void d0m1PopupKey(int key);
extern void d0m1PopupResign(void);
extern void d0m1PopupClosed(void);
extern void d0m1PopupOutside(void);
extern int d0m1WindowHover(int x, int y);
extern void d0m1WindowClick(int x, int y);
extern void d0m1WindowKey(int key);
extern void d0m1WindowVisible(int on);
extern void d0m1WindowResign(void);

int d0m1Pad = 12;
int d0m1LineH = 0;
int d0m1RuleH = 9;
int d0m1ActionH = 0;
double d0m1CharW = 0;
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
	// The monospace advance, for the column under the pointer (links).
	d0m1CharW = [@"0000000000" sizeWithAttributes:@{NSFontAttributeName : d0m1Mono}].width / 10.0;
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
// green "copied"; its link spans (l1..l1End, l2..l2End) are drawn as links. selected is
// the action-row highlight.
static NSAttributedString *d0m1LineAttr(NSString *body, int kind, int age, int ageEnd, int hi, int hiEnd, int quiet, int selected,
	int l1, int l1End, int l2, int l2End) {
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
	} else if (kind == 3 && (l1End > l1 || l2End > l2)) {
		int spans[2][2] = {{l1, l1End}, {l2, l2End}};
		NSUInteger at = 0;
		for (int k = 0; k < 2; k++) {
			if (spans[k][1] <= spans[k][0] || spans[k][0] < (int)at) continue;
			d0m1Append(s, body, at, (NSUInteger)spans[k][0], d0m1Mono, c);
			d0m1Append(s, body, (NSUInteger)spans[k][0], (NSUInteger)spans[k][1], d0m1Mono, d0m1RGB(0x58a6ff));
			at = (NSUInteger)spans[k][1];
		}
		d0m1Append(s, body, at, body.length, d0m1Mono, c);
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
// owner: 0 the panel or the popup, 1 the main window (its rows call the
// window's callbacks). flat: square and edgeless (the window frames it).
@property int owner;
@property BOOL flat;
@property NSTrackingArea *track;
@end

@implementation D0m1SheetView
- (BOOL)acceptsFirstResponder { return self.interactive; }
// The main window's rows act on the click that also activates it.
- (BOOL)acceptsFirstMouse:(NSEvent *)e { return self.owner == 1; }
- (int)rowH:(int)kind {
	if (kind == 4) return self.ruleH;
	if (kind >= 5 && kind <= 7) return self.actionH;
	return self.lineH;
}
- (int)yOf:(NSEvent *)e {
	NSPoint p = [self convertPoint:e.locationInWindow fromView:nil];
	return (int)llround(self.bounds.size.height - p.y);
}
- (int)xOf:(NSEvent *)e {
	return (int)llround([self convertPoint:e.locationInWindow fromView:nil].x);
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
	int x = [self xOf:e], y = [self yOf:e];
	int hand = self.owner == 1 ? d0m1WindowHover(x, y) : d0m1PopupHover(x, y);
	if (hand) [[NSCursor pointingHandCursor] set];
	else [[NSCursor arrowCursor] set];
}
- (void)mouseExited:(NSEvent *)e {
	if (self.interactive) {
		if (self.owner == 1) d0m1WindowHover(-1, -1);
		else d0m1PopupHover(-1, -1);
	}
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
	if (!self.interactive) return;
	if (self.owner == 1) d0m1WindowClick([self xOf:e], [self yOf:e]);
	else d0m1PopupClick([self xOf:e], [self yOf:e]);
}
- (void)keyDown:(NSEvent *)e {
	if (!self.interactive) { [super keyDown:e]; return; }
	int key = 0;
	switch (e.keyCode) {
	case 126: key = 1; break;                  // up
	case 125: case 48: key = 2; break;         // down, tab
	case 36: case 76: case 49: key = 3; break; // return, keypad enter, space
	case 53: key = 4; break;                   // esc
	default: break;
	}
	if (key == 0) return;
	if (self.owner == 1) d0m1WindowKey(key);
	else d0m1PopupKey(key);
}
- (void)drawRect:(NSRect)dirty {
	NSRect b = self.bounds;
	NSBezierPath *round = self.flat ? [NSBezierPath bezierPathWithRect:b] : [NSBezierPath bezierPathWithRoundedRect:b xRadius:8 yRadius:8];
	[NSGraphicsContext.currentContext saveGraphicsState];
	[round addClip];
	[(self.flat ? d0m1RGB(0x0d1410) : [d0m1RGB(0x0d1410) colorWithAlphaComponent:0.96]) setFill];
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
				int age = 0, ageEnd = 0, hi = 0, hiEnd = 0, quiet = 0, selected = 0, l1 = 0, l1End = 0, l2 = 0, l2End = 0;
				if (i < spanLines.count) {
					sscanf(spanLines[i].UTF8String, "%d,%d,%d,%d,%d,%d,%d,%d,%d,%d", &age, &ageEnd, &hi, &hiEnd, &quiet, &selected,
						&l1, &l1End, &l2, &l2End);
				}
				if (selected) {
					[d0m1RGB(0x17331f) setFill];
					NSRectFill(NSMakeRect(self.pad - 6, rowBottom, b.size.width - 2 * (self.pad - 6), rh));
				}
				NSAttributedString *attr = d0m1LineAttr(lines[i], kind, age, ageEnd, hi, hiEnd, quiet, selected, l1, l1End, l2, l2End);
				NSSize ts = attr.size;
				[attr drawInRect:NSMakeRect(self.pad, rowBottom + (rh - ts.height) / 2.0, ts.width, ts.height)];
			}
			yTop += rh;
		}
	}
	[NSGraphicsContext.currentContext restoreGraphicsState];
	if (self.flat) return;
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

// The main window (window mode): a normal titled window with the same sheet
// view, interactive like the popup's. Closing it hides it (the app keeps
// collecting; the Dock icon shows it again); Cmd-Q or the Quit row quits.
@interface D0m1MainDelegate : NSObject <NSWindowDelegate>
@end
static NSWindow *d0m1Main;
static D0m1SheetView *d0m1MainView;
static D0m1MainDelegate *d0m1MainDel;

@implementation D0m1MainDelegate
- (BOOL)windowShouldClose:(NSWindow *)sender {
	[sender orderOut:nil];
	d0m1WindowVisible(0);
	return NO;
}
- (void)windowDidMiniaturize:(NSNotification *)n { d0m1WindowVisible(0); }
- (void)windowDidDeminiaturize:(NSNotification *)n { d0m1WindowVisible(1); }
- (void)windowDidResignKey:(NSNotification *)n { d0m1WindowResign(); }
@end

// d0m1MainUpdate creates the window (hidden) on first use, draws the rows
// and fits the window to them, keeping its top edge. front shows it,
// deminiaturized, as the key window of the active app.
static void d0m1MainUpdate(const char *title, const char *text, const char *kinds, const char *spans,
	int pad, int lineH, int ruleH, int actionH, int w, int h, int front) {
	NSString *ti = [NSString stringWithUTF8String:title ? title : ""];
	NSString *t = [NSString stringWithUTF8String:text ? text : ""];
	NSString *k = [NSString stringWithUTF8String:kinds ? kinds : ""];
	NSString *sp = [NSString stringWithUTF8String:spans ? spans : ""];
	d0m1OnMain(^{
		if (!d0m1Main) {
			d0m1Main = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, w, h)
				styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable
				backing:NSBackingStoreBuffered defer:NO];
			d0m1Main.title = ti;
			d0m1Main.releasedWhenClosed = NO;
			d0m1Main.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
			d0m1Main.backgroundColor = d0m1RGB(0x0d1410);
			d0m1Main.titlebarAppearsTransparent = YES;
			d0m1Main.acceptsMouseMovedEvents = YES;
			d0m1MainView = [[D0m1SheetView alloc] initWithFrame:NSMakeRect(0, 0, w, h)];
			d0m1MainView.interactive = YES;
			d0m1MainView.owner = 1;
			d0m1MainView.flat = YES;
			d0m1MainView.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
			d0m1Main.contentView = d0m1MainView;
			d0m1MainDel = [[D0m1MainDelegate alloc] init];
			d0m1Main.delegate = d0m1MainDel;
			[d0m1Main center];
		}
		d0m1Fill(d0m1MainView, nil, t, k, sp, pad, lineH, ruleH, actionH, w, h);
		NSRect content = [d0m1Main contentRectForFrameRect:d0m1Main.frame];
		if ((int)llround(content.size.width) != w || (int)llround(content.size.height) != h) {
			NSRect c = NSMakeRect(content.origin.x, NSMaxY(content) - h, w, h);
			[d0m1Main setFrame:[d0m1Main frameRectForContentRect:c] display:YES];
		}
		if (front) {
			if (d0m1Main.miniaturized) [d0m1Main deminiaturize:nil];
			[NSApp activateIgnoringOtherApps:YES];
			[d0m1Main makeKeyAndOrderFront:nil];
			[d0m1Main makeFirstResponder:d0m1MainView];
		}
	});
}

// Hide (Cmd-H, the app menu's Hide) takes the main window off screen
// without closing or miniaturizing it: the app hears that it is off screen
// (no redraws while hidden), and Unhide brings back what was shown.
static BOOL d0m1MainShownAtHide;
static id d0m1HideObservers[3];

static void d0m1ObserveHide(void) {
	if (d0m1HideObservers[0]) return;
	NSNotificationCenter *nc = NSNotificationCenter.defaultCenter;
	d0m1HideObservers[0] = [nc addObserverForName:NSApplicationWillHideNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		d0m1MainShownAtHide = d0m1Main && d0m1Main.visible && !d0m1Main.miniaturized;
	}];
	d0m1HideObservers[1] = [nc addObserverForName:NSApplicationDidHideNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		if (d0m1MainShownAtHide) d0m1WindowVisible(0);
	}];
	d0m1HideObservers[2] = [nc addObserverForName:NSApplicationDidUnhideNotification object:nil queue:nil usingBlock:^(NSNotification *n) {
		if (d0m1MainShownAtHide) d0m1WindowVisible(1);
		d0m1MainShownAtHide = NO;
	}];
}

static void d0m1MainHide(void) {
	d0m1OnMain(^{
		[d0m1Main orderOut:nil];
	});
}

// The main window's state for app.dump: 1 exists, 2 visible, 4 miniaturized,
// 8 key.
static int d0m1MainState(void) {
	__block int s = 0;
	d0m1OnMain(^{
		if (!d0m1Main) return;
		s = 1;
		if (d0m1Main.visible) s |= 2;
		if (d0m1Main.miniaturized) s |= 4;
		if (d0m1Main.keyWindow) s |= 8;
	});
	return s;
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

// setActivation runs on the main thread (systray locks it at init). Window
// mode is a regular app: a Dock icon (the status dot), an app menu with
// Quit (Cmd-Q), and a click on the Dock icon shows the main window. Tray
// only, it is a menu-bar accessory, as before.
func setActivation(window bool) {
	if !window {
		C.d0m1SetAccessory()
		return
	}
	name := C.CString(tray.WindowTitle)
	defer C.free(unsafe.Pointer(name))
	C.d0m1SetRegular(name)
	setDockIcon(tray.Green)
}

// replyTerminate ends a termination macOS asked for (the Dock menu's Quit,
// a logout), which the app's quit answers: true when there was one (the
// process then exits), false for a quit of the app's own (stop the loop).
func replyTerminate() bool { return C.d0m1ReplyTerminate() != 0 }

// setDockIcon draws the Dock icon in c.
func setDockIcon(c tray.Color) {
	b := tray.IconPNG(c, 256, tray.DockInset)
	C.d0m1SetDockIcon(unsafe.Pointer(&b[0]), C.int(len(b)))
}

// showMainNative draws the main window's rows (creating it, hidden, the
// first time); front brings it to the front.
func showMainNative(text, kinds, spans string, m tray.Metrics, w, h int, front bool) {
	ti, ct, ck, cs := cstr(tray.WindowTitle), cstr(text), cstr(kinds), cstr(spans)
	defer C.free(unsafe.Pointer(ti))
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(ck))
	defer C.free(unsafe.Pointer(cs))
	f := 0
	if front {
		f = 1
	}
	C.d0m1MainUpdate(ti, ct, ck, cs, C.int(m.Pad), C.int(m.LineH), C.int(m.RuleH), C.int(m.ActionH),
		C.int(w), C.int(h), C.int(f))
}

func hideMainNative() { C.d0m1MainHide() }

// mainNativeState is the main window as AppKit has it.
func mainNativeState() (exists, visible, miniaturized, key bool) {
	s := int(C.d0m1MainState())
	return s&1 != 0, s&2 != 0, s&4 != 0, s&8 != 0
}

// confirm is a native NSAlert (called from a click goroutine; the alert
// itself runs on the main thread).
func confirm(question string) bool {
	q := C.CString(question)
	defer C.free(unsafe.Pointer(q))
	return C.d0m1Confirm(q) == 1
}

func sheetMetrics() tray.Metrics {
	C.d0m1Metrics()
	return tray.Metrics{Pad: int(C.d0m1Pad), LineH: int(C.d0m1LineH), RuleH: int(C.d0m1RuleH), ActionH: int(C.d0m1ActionH), CharW: float64(C.d0m1CharW)}
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
		var links [4]int
		for k, ln := range l.Links {
			if ln.Action != tray.ActNone {
				links[2*k], links[2*k+1] = ln.From, ln.To
			}
		}
		fmt.Fprintf(&sb, "%d,%d,%d,%d,%d,%d,%d,%d,%d,%d", l.Age, l.AgeEnd, l.Hi, l.HiEnd, quiet, sel, links[0], links[1], links[2], links[3])
	}
	return tb.String(), kb.String(), sb.String()
}

// sheetBox is the sheet's size. wider only stretches the width (the popup's
// "copied" and armed-Quit lines), so showing them does not resize it. close
// leaves room for the × beside the first line (not in the main window).
func sheetBox(lines, wider []tray.PanelLine, close bool) (text, kinds, spans string, m tray.Metrics, w, h int) {
	m = sheetMetrics()
	text, kinds, spans = encodeLines(lines)
	maxW := 0
	for _, set := range [][]tray.PanelLine{lines, wider} {
		for i, l := range set {
			if l.Kind == tray.LineRule {
				continue
			}
			tw := textWidth(l.Text)
			if i == 0 && close {
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

	text, kinds, spans, m, w, ht := sheetBox(lines, lines, true)
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
