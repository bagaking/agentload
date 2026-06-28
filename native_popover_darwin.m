// native_popover_darwin.m implements the native macOS popover for
// AgentLoad.
//
// Architecture mirrors the public AppKit + WebKit pattern any tutorial
// covers: an NSPanel hosting a WKWebView pointed at the loopback popover
// server, anchored against the real NSStatusItem button frame. We never
// copy OnWatch's GPL source — this file is written from scratch against
// the public AppKit / WebKit APIs.
//
// Key decisions:
//   * Anchor to the systray status button: walk NSApp.delegate.statusItem
//     (fyne.io/systray's ivar) via KVC so the popover lines up with the
//     icon the user actually clicked, on the screen the menu bar lives
//     on (multi-monitor + notch safe). Falls back to cursor-screen center
//     if the systray internals ever change shape.
//   * Outside-click dismissal goes through three signals: a global mouse
//     monitor (clicks in other apps), a local mouse monitor (clicks
//     inside our process, e.g. another app window), and an
//     NSApplicationDidResignActiveNotification observer (Cmd-Tab away).
//     The "is the click inside us?" check intentionally treats the
//     status item button frame as part of the popover, so the second
//     click on the icon doesn't race the systray toggle and immediately
//     reopen / reclose the panel.
//   * WKNavigationDelegate only allows the exact popover origin inside
//     WKWebView. Other loopback URLs are rejected and external URLs route to
//     NSWorkspace. The same external routing covers target=_blank popups via
//     createWebViewWithConfiguration.
//   * WKScriptMessageHandler exposes two channels for the embedded HTML:
//     `agentLoadResize` (clamped to the current screen) and `agentLoadAction`
//     (close / open_dashboard / quit).

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import "native_popover_darwin.h"

// Borderless non-activating panels are not key-eligible by default. We need
// keyboard input to reach the settings drawer so we override the standard
// "no" answers. Pairing this with becomesKeyOnlyIfNeeded keeps the panel
// from stealing focus until an interactive control actually demands it.
@interface AgentLoadPopoverPanel : NSPanel
@end

@implementation AgentLoadPopoverPanel
- (BOOL)canBecomeKeyWindow { return YES; }
- (BOOL)canBecomeMainWindow { return NO; }
- (BOOL)acceptsFirstMouse:(NSEvent *)event { return YES; }
@end

@interface AgentLoadMenubarPopover : NSObject <WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler>
@property (nonatomic, strong) AgentLoadPopoverPanel *panel;
@property (nonatomic, strong) WKWebView *webView;
@property (nonatomic, copy)   NSString *cookieName;
@property (nonatomic, copy)   NSString *cookieValue;
@property (nonatomic, copy)   NSString *dashboardURL;
@property (nonatomic, copy)   NSString *pendingURL;
@property (nonatomic, copy)   NSString *loadedURL;
@property (nonatomic, copy)   NSString *popoverOrigin;
@property (nonatomic, copy)   NSString *statusClickFallbackURL;
@property (nonatomic, assign) BOOL cookieReady;
@property (nonatomic, assign) BOOL hasLastStatusClickPoint;
@property (nonatomic, assign) CGFloat width;
@property (nonatomic, assign) CGFloat height;
@property (nonatomic, assign) NSPoint lastStatusClickPoint;
@property (nonatomic, assign) NSTimeInterval lastStatusClickAt;
@property (nonatomic, strong) id globalMonitor;
@property (nonatomic, strong) id localMonitor;
@property (nonatomic, strong) id statusClickFallbackMonitor;
@property (nonatomic, strong) id deactivationObserver;
@end

@implementation AgentLoadMenubarPopover

+ (instancetype)shared {
    static AgentLoadMenubarPopover *instance;
    static dispatch_once_t once;
    dispatch_once(&once, ^{ instance = [[self alloc] init]; });
    return instance;
}

- (instancetype)init {
    self = [super init];
    if (self != nil) {
        self.width = 380.0;
        self.height = 560.0;
    }
    return self;
}

- (void)setupIfNeeded {
    if (self.panel != nil) return;

    [self ensureApplicationCanPresentUI];

    NSRect frame = NSMakeRect(0, 0, self.width, self.height);

    WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
    // Use a non-persistent data store so cookies never reach disk.
    config.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];

    WKUserContentController *userContent = [[WKUserContentController alloc] init];
    [userContent addScriptMessageHandler:self name:@"agentLoadResize"];
    [userContent addScriptMessageHandler:self name:@"agentLoadAction"];
    config.userContentController = userContent;

    WKWebView *web = [[WKWebView alloc] initWithFrame:frame configuration:config];
    web.translatesAutoresizingMaskIntoConstraints = YES;
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    web.navigationDelegate = self;
    web.UIDelegate = self;
    web.wantsLayer = YES;
    web.layer.backgroundColor = [NSColor clearColor].CGColor;
    web.layer.cornerRadius = 12.0;
    web.layer.masksToBounds = YES;
    web.layer.opaque = NO;
    web.enclosingScrollView.drawsBackground = NO;
    if ([web respondsToSelector:@selector(setValue:forKey:)]) {
        @try {
            [web setValue:@NO forKey:@"drawsBackground"];
        } @catch (__unused NSException *exception) {
            // Older WebKit builds do not expose drawsBackground on WKWebView.
        }
    }

    NSWindowStyleMask mask = NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel;
    AgentLoadPopoverPanel *panel = [[AgentLoadPopoverPanel alloc]
        initWithContentRect:frame
                  styleMask:mask
                    backing:NSBackingStoreBuffered
                      defer:YES];
    panel.floatingPanel = YES;
    panel.becomesKeyOnlyIfNeeded = YES;
    // hidesOnDeactivate must stay NO. A non-activating panel never owns app
    // activation, and YES would race with our explicit deactivation observer
    // below — letting AppKit hide the panel before we capture the dismissal
    // signal we want to act on.
    panel.hidesOnDeactivate = NO;
    panel.releasedWhenClosed = NO;
    panel.opaque = NO;
    panel.backgroundColor = [NSColor clearColor];
    panel.hasShadow = YES;
    panel.level = NSStatusWindowLevel;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces
        | NSWindowCollectionBehaviorFullScreenAuxiliary;
    panel.contentView = web;

    self.panel = panel;
    self.webView = web;
}

- (void)ensureApplicationCanPresentUI {
    // CLI LaunchAgents start as background-only processes. That is enough for
    // an NSStatusItem, but AppKit can refuse to present windows for that
    // activation policy. Accessory keeps the app out of the Dock while allowing
    // the popover panel to appear.
    if (NSApp.activationPolicy == NSApplicationActivationPolicyProhibited) {
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    }
}

#pragma mark - Status item anchoring

// statusItemFromAppDelegate walks NSApp.delegate via KVC to recover the
// fyne.io/systray status item. The delegate stores the NSStatusItem as the
// instance variable `statusItem`; valueForKey: falls through to ivar access
// because NSObject's accessInstanceVariablesDirectly is YES by default. We
// keep the lookup defensive — if a future systray version renames the ivar
// the popover transparently falls back to cursor-screen centering.
- (NSStatusItem *)statusItemFromAppDelegate {
    id delegate = NSApp.delegate;
    if (delegate == nil) return nil;
    id value = nil;
    @try {
        value = [delegate valueForKey:@"statusItem"];
    } @catch (NSException *e) {
        return nil;
    }
    if (![value isKindOfClass:[NSStatusItem class]]) return nil;
    return (NSStatusItem *)value;
}

- (NSRect)statusButtonScreenRect {
    NSStatusItem *item = [self statusItemFromAppDelegate];
    NSStatusBarButton *button = item.button;
    if (button == nil || button.window == nil) return NSZeroRect;
    NSRect rectInWindow = [button convertRect:button.bounds toView:nil];
    return [button.window convertRectToScreen:rectInWindow];
}

- (NSScreen *)screenForRect:(NSRect)rect {
    NSPoint center = NSMakePoint(NSMidX(rect), NSMidY(rect));
    return [self screenForPoint:center];
}

- (NSScreen *)screenForPoint:(NSPoint)point {
    for (NSScreen *s in [NSScreen screens]) {
        if (NSPointInRect(point, s.frame)) return s;
    }
    return [NSScreen mainScreen];
}

- (BOOL)isMenuBarScreenPoint:(NSPoint)point {
    NSScreen *screen = [self screenForPoint:point];
    if (screen == nil) return NO;
    return point.y >= NSMaxY(screen.frame) - 40.0;
}

- (BOOL)hasFreshStatusClickPoint {
    if (!self.hasLastStatusClickPoint) return NO;
    return [NSDate timeIntervalSinceReferenceDate] - self.lastStatusClickAt < 2.0;
}

- (NSRect)recentStatusClickRect {
    if (![self hasFreshStatusClickPoint]) return NSZeroRect;
    NSPoint p = self.lastStatusClickPoint;
    return NSMakeRect(p.x - 12.0, p.y - 11.0, 24.0, 22.0);
}

- (void)rememberStatusClickPoint:(NSPoint)point {
    if (![self isMenuBarScreenPoint:point]) return;
    self.lastStatusClickPoint = point;
    self.lastStatusClickAt = [NSDate timeIntervalSinceReferenceDate];
    self.hasLastStatusClickPoint = YES;
}

- (NSRect)normalizedStatusButtonRect:(NSRect)button {
    if (NSIsEmptyRect(button) || [self hasFreshStatusClickPoint]) return button;
    NSScreen *mainScreen = [NSScreen mainScreen];
    if (mainScreen == nil || [self isMenuBarScreenPoint:NSMakePoint(NSMidX(button), NSMidY(button))]) return button;

    NSRect visible = mainScreen.visibleFrame;
    CGFloat buttonWidth = MAX(24.0, NSWidth(button));
    CGFloat midX = NSMidX(button);
    CGFloat minMidX = NSMinX(visible) + buttonWidth / 2.0;
    CGFloat maxMidX = NSMaxX(visible) - buttonWidth / 2.0;
    if (maxMidX < minMidX) maxMidX = minMidX;
    if (midX < minMidX) midX = minMidX;
    if (midX > maxMidX) midX = maxMidX;

    return NSMakeRect(midX - buttonWidth / 2.0, NSMaxY(visible), buttonWidth, 22.0);
}

- (void)positionPanelAnchored {
    NSRect button = [self normalizedStatusButtonRect:[self statusButtonScreenRect]];
    NSRect recentClick = [self recentStatusClickRect];
    if (!NSIsEmptyRect(recentClick)) {
        NSScreen *buttonScreen = NSIsEmptyRect(button) ? nil : [self screenForRect:button];
        NSScreen *clickScreen = [self screenForRect:recentClick];
        if (buttonScreen == nil || buttonScreen != clickScreen || !NSPointInRect(self.lastStatusClickPoint, button)) {
            button = recentClick;
        }
    }
    NSScreen *screen = nil;
    NSRect visible;
    CGFloat width = self.width;
    CGFloat height = self.height;
    CGFloat targetX;
    CGFloat targetY;

    if (NSIsEmptyRect(button)) {
        // Fallback only triggers if KVC into the systray delegate fails —
        // we still want to land on whatever screen the cursor is on so a
        // multi-monitor user does not see the popover on the wrong display.
        NSPoint cursor = [NSEvent mouseLocation];
        for (NSScreen *s in [NSScreen screens]) {
            if (NSPointInRect(cursor, s.frame)) { screen = s; break; }
        }
        if (screen == nil) screen = [NSScreen mainScreen];
        if (screen == nil) return;
        visible = screen.visibleFrame;
        targetX = NSMidX(visible) - width / 2.0;
        targetY = NSMaxY(visible) - height - 6.0;
    } else {
        screen = [self screenForRect:button];
        if (screen == nil) return;
        visible = screen.visibleFrame;
        targetX = NSMidX(button) - width / 2.0;
        targetY = NSMinY(button) - height - 6.0;
    }

    CGFloat minX = NSMinX(visible);
    CGFloat maxX = NSMaxX(visible) - width;
    if (maxX < minX) maxX = minX;
    if (targetX < minX) targetX = minX;
    if (targetX > maxX) targetX = maxX;

    CGFloat minY = NSMinY(visible);
    CGFloat maxY = NSMaxY(visible) - height;
    if (maxY < minY) maxY = minY;
    if (targetY < minY) targetY = minY;
    if (targetY > maxY) targetY = maxY;

    NSRect frame = NSMakeRect(round(targetX), round(targetY), width, height);
    [self.panel setFrame:frame display:YES];
}

- (void)applyHeight:(CGFloat)height {
    CGFloat maxHeight = [self maxPopoverHeight];
    CGFloat clamped = MAX(140.0, MIN(maxHeight, height));
    if (fabs(clamped - self.height) < 0.5) return;
    self.height = clamped;
    NSSize size = NSMakeSize(self.width, clamped);
    [self.panel setContentSize:size];
    if (self.panel.isVisible) {
        [self positionPanelAnchored];
    }
}

- (CGFloat)maxPopoverHeight {
    NSScreen *screen = nil;
    NSRect button = [self statusButtonScreenRect];
    if (!NSIsEmptyRect(button)) {
        screen = [self screenForRect:button];
    }
    if (screen == nil) {
        NSPoint cursor = [NSEvent mouseLocation];
        for (NSScreen *s in [NSScreen screens]) {
            if (NSPointInRect(cursor, s.frame)) { screen = s; break; }
        }
    }
    if (screen == nil) screen = [NSScreen mainScreen];
    if (screen == nil) return 600.0;
    CGFloat available = NSHeight(screen.visibleFrame) - 18.0;
    return MAX(140.0, MIN(600.0, available));
}

#pragma mark - Outside dismissal

- (BOOL)containsScreenPoint:(NSPoint)point {
    if (self.panel.isVisible && NSPointInRect(point, self.panel.frame)) return YES;
    // Treat the status item button as "inside" so a second click on the icon
    // doesn't race with the systray toggle: if both fire, the toggle wins
    // cleanly without the global monitor reopening / reclosing the panel.
    return [self isStatusButtonScreenPoint:point];
}

- (BOOL)isStatusButtonScreenPoint:(NSPoint)point {
    NSRect button = [self statusButtonScreenRect];
    return !NSIsEmptyRect(button) && NSPointInRect(point, button);
}

- (NSPoint)screenPointForEvent:(NSEvent *)event {
    NSPoint p = event.locationInWindow;
    if (event.window != nil) {
        p = [event.window convertPointToScreen:p];
    }
    return p;
}

- (void)closeIfOutside:(NSPoint)point {
    if (!self.panel.isVisible) return;
    if ([self containsScreenPoint:point]) return;
    [self stopCloseMonitoring];
    [self notifyPopoverHidden];
    [self.panel orderOut:nil];
}

- (void)startCloseMonitoring {
    [self stopCloseMonitoring];
    NSEventMask mask = NSEventMaskLeftMouseDown
        | NSEventMaskRightMouseDown
        | NSEventMaskOtherMouseDown;

    __weak typeof(self) weakSelf = self;

    self.globalMonitor = [NSEvent addGlobalMonitorForEventsMatchingMask:mask
        handler:^(NSEvent *event) {
            __strong typeof(weakSelf) self_ = weakSelf;
            if (self_ == nil) return;
            // Global monitors fire only for events outside our process, so
            // event.window is nil and we use the live cursor location.
            NSPoint screenPoint = [NSEvent mouseLocation];
            dispatch_async(dispatch_get_main_queue(), ^{
                [self_ closeIfOutside:screenPoint];
            });
        }];

    self.localMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:mask
        handler:^NSEvent *(NSEvent *event) {
            __strong typeof(weakSelf) self_ = weakSelf;
            if (self_ == nil) return event;
            NSPoint screenPoint = [self_ screenPointForEvent:event];
            [self_ closeIfOutside:screenPoint];
            return event;
        }];

    self.deactivationObserver = [[NSNotificationCenter defaultCenter]
        addObserverForName:NSApplicationDidResignActiveNotification
                    object:NSApp
                     queue:[NSOperationQueue mainQueue]
                usingBlock:^(NSNotification *note) {
                    __strong typeof(weakSelf) self_ = weakSelf;
                    if (self_ == nil) return;
                    [self_ stopCloseMonitoring];
                    [self_ notifyPopoverHidden];
                    [self_.panel orderOut:nil];
                }];
}

- (void)stopCloseMonitoring {
    if (self.globalMonitor != nil) {
        [NSEvent removeMonitor:self.globalMonitor];
        self.globalMonitor = nil;
    }
    if (self.localMonitor != nil) {
        [NSEvent removeMonitor:self.localMonitor];
        self.localMonitor = nil;
    }
    if (self.deactivationObserver != nil) {
        [[NSNotificationCenter defaultCenter] removeObserver:self.deactivationObserver];
        self.deactivationObserver = nil;
    }
}

- (void)installStatusClickFallback:(NSString *)url {
    self.statusClickFallbackURL = url ? [url copy] : @"";
    if (self.statusClickFallbackURL.length == 0) {
        [self removeStatusClickFallback];
        return;
    }
    if (self.statusClickFallbackMonitor != nil) return;

    __weak typeof(self) weakSelf = self;
    self.statusClickFallbackMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown
        handler:^NSEvent *(NSEvent *event) {
            __strong typeof(weakSelf) self_ = weakSelf;
            if (self_ == nil) return event;
            NSPoint screenPoint = [self_ screenPointForEvent:event];
            if (event.window != self_.panel) {
                [self_ rememberStatusClickPoint:screenPoint];
            }
            if (![self_ isStatusButtonScreenPoint:screenPoint]) return event;

            NSString *target = [self_.statusClickFallbackURL copy];
            BOOL wasVisible = self_.panel != nil && self_.panel.isVisible;
            dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.15 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
                __strong typeof(weakSelf) self__ = weakSelf;
                if (self__ == nil || target.length == 0) return;
                BOOL isVisible = self__.panel != nil && self__.panel.isVisible;
                if (wasVisible) {
                    if (isVisible) [self__ hide];
                    return;
                }
                if (!isVisible) [self__ showURL:target];
            });
            return event;
        }];
}

- (void)removeStatusClickFallback {
    if (self.statusClickFallbackMonitor != nil) {
        [NSEvent removeMonitor:self.statusClickFallbackMonitor];
        self.statusClickFallbackMonitor = nil;
    }
    self.statusClickFallbackURL = @"";
}

#pragma mark - Public API

- (void)configureCookie:(NSString *)name value:(NSString *)value {
    self.cookieName = name ? [name copy] : nil;
    self.cookieValue = value ? [value copy] : nil;
    self.cookieReady = NO;
}

- (void)configureDashboard:(NSString *)url {
    self.dashboardURL = url ? [url copy] : nil;
}

- (void)loadAndShow:(NSString *)url {
    NSURL *u = [NSURL URLWithString:url];
    if (u == nil) return;
    [self ensureApplicationCanPresentUI];
    self.popoverOrigin = [self originForURL:u];
    if (![self.loadedURL isEqualToString:url]) {
        NSURLRequest *req = [NSURLRequest requestWithURL:u
            cachePolicy:NSURLRequestUseProtocolCachePolicy
            timeoutInterval:8.0];
        self.loadedURL = [url copy];
        [self.webView loadRequest:req];
    }
    [self positionPanelAnchored];
    if (!self.panel.isVisible) {
        // LaunchAgent-started CLI binaries are registered as background-only
        // apps. On newer macOS builds makeKeyAndOrderFront can create the
        // WebKit view without making the non-activating panel visible, so we
        // explicitly order it above other windows and then make it key for
        // settings inputs.
        [self.panel orderFrontRegardless];
        [self.panel makeKeyWindow];
    }
    [self notifyPopoverShown];
    [self startCloseMonitoring];
}

- (void)showURL:(NSString *)url {
    if (NSClassFromString(@"WKWebView") == nil) return;
    [self setupIfNeeded];

    // Click-to-toggle: a second click on the icon hides the popover. We
    // tear monitoring down explicitly so a closed popover doesn't keep an
    // NSEvent handler alive in the background.
    if (self.panel.isVisible) {
        [self stopCloseMonitoring];
        [self notifyPopoverHidden];
        [self.panel orderOut:nil];
        return;
    }

    if (self.cookieName.length > 0 && self.cookieValue.length > 0) {
        NSDictionary *props = @{
            NSHTTPCookieName:    self.cookieName,
            NSHTTPCookieValue:   self.cookieValue,
            NSHTTPCookieDomain:  @"127.0.0.1",
            NSHTTPCookiePath:    @"/",
            NSHTTPCookieDiscard: @YES,
        };
        NSHTTPCookie *cookie = [NSHTTPCookie cookieWithProperties:props];
        if (cookie != nil) {
            self.pendingURL = url;
            __weak typeof(self) weakSelf = self;
            [self.webView.configuration.websiteDataStore.httpCookieStore
                setCookie:cookie
                completionHandler:^{
                    __strong typeof(weakSelf) self_ = weakSelf;
                    if (self_ == nil) return;
                    self_.cookieReady = YES;
                    dispatch_async(dispatch_get_main_queue(), ^{
                        [self_ loadAndShow:self_.pendingURL ?: url];
                    });
                }];
            return;
        }
    }
    [self loadAndShow:url];
}

- (void)hide {
    [self stopCloseMonitoring];
    if (self.panel != nil && self.panel.isVisible) {
        [self notifyPopoverHidden];
        [self.panel orderOut:nil];
    }
}

- (void)notifyPopoverShown {
    [self.webView evaluateJavaScript:@"window.dispatchEvent(new Event('agentLoadPopoverShown'));" completionHandler:nil];
}

- (void)notifyPopoverHidden {
    [self.webView evaluateJavaScript:@"window.dispatchEvent(new Event('agentLoadPopoverHidden'));" completionHandler:nil];
}

#pragma mark - WKNavigationDelegate

- (NSString *)originForURL:(NSURL *)url {
    if (url == nil || url.scheme.length == 0 || url.host.length == 0) return nil;
    NSString *scheme = [url.scheme lowercaseString];
    NSString *host = [url.host lowercaseString];
    NSNumber *port = url.port;
    NSString *portPart = port != nil ? [NSString stringWithFormat:@":%@", port] : @"";
    return [NSString stringWithFormat:@"%@://%@%@", scheme, host, portPart];
}

- (BOOL)isTrustedPopoverURL:(NSURL *)url {
    if (url == nil) return NO;
    NSString *scheme = [url.scheme lowercaseString];
    if ([scheme isEqualToString:@"about"]) return YES;
    NSString *origin = [self originForURL:url];
    return origin.length > 0 && self.popoverOrigin.length > 0 && [origin isEqualToString:self.popoverOrigin];
}

- (BOOL)isLoopbackHostURL:(NSURL *)url {
    if (url == nil) return NO;
    NSString *host = [url.host lowercaseString];
    if ([host isEqualToString:@"127.0.0.1"]) return YES;
    if ([host isEqualToString:@"::1"]) return YES;
    if ([host isEqualToString:@"localhost"]) return YES;
    return NO;
}

- (void)webView:(WKWebView *)webView
    decidePolicyForNavigationAction:(WKNavigationAction *)navigationAction
                    decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
    NSURL *url = navigationAction.request.URL;
    if ([self isTrustedPopoverURL:url]) {
        decisionHandler(WKNavigationActionPolicyAllow);
        return;
    }
    if ([self isLoopbackHostURL:url]) {
        decisionHandler(WKNavigationActionPolicyCancel);
        return;
    }
    if (url != nil) {
        [[NSWorkspace sharedWorkspace] openURL:url];
    }
    decisionHandler(WKNavigationActionPolicyCancel);
}

#pragma mark - WKUIDelegate

- (WKWebView *)webView:(WKWebView *)webView
    createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
               forNavigationAction:(WKNavigationAction *)navigationAction
                    windowFeatures:(WKWindowFeatures *)windowFeatures {
    NSURL *url = navigationAction.request.URL;
    if ([self isLoopbackHostURL:url]) {
        return nil;
    }
    if (url != nil) {
        [[NSWorkspace sharedWorkspace] openURL:url];
    }
    return nil;
}

#pragma mark - WKScriptMessageHandler

- (void)userContentController:(WKUserContentController *)userContentController
      didReceiveScriptMessage:(WKScriptMessage *)message {
    if ([message.name isEqualToString:@"agentLoadResize"]) {
        CGFloat next = self.height;
        id body = message.body;
        if ([body isKindOfClass:[NSNumber class]]) {
            next = [body doubleValue];
        } else if ([body isKindOfClass:[NSDictionary class]]) {
            id v = [(NSDictionary *)body objectForKey:@"height"];
            if ([v respondsToSelector:@selector(doubleValue)]) {
                next = [v doubleValue];
            }
        }
        [self applyHeight:next];
        return;
    }

    if ([message.name isEqualToString:@"agentLoadAction"]) {
        NSString *action = nil;
        id body = message.body;
        if ([body isKindOfClass:[NSString class]]) {
            action = (NSString *)body;
        } else if ([body isKindOfClass:[NSDictionary class]]) {
            id v = [(NSDictionary *)body objectForKey:@"action"];
            if ([v isKindOfClass:[NSString class]]) action = (NSString *)v;
        }
        if (![action isKindOfClass:[NSString class]]) return;

        if ([action isEqualToString:@"close"]) {
            [self hide];
            return;
        }
        if ([action isEqualToString:@"open_dashboard"]) {
            NSString *target = [self.dashboardURL copy];
            if (target.length == 0) return;
            NSURL *u = [NSURL URLWithString:target];
            if (u == nil) return;
            [[NSWorkspace sharedWorkspace] openURL:u];
            [self hide];
            return;
        }
        if ([action isEqualToString:@"quit"]) {
            [self hide];
            [[NSApplication sharedApplication] terminate:nil];
            return;
        }
    }
}

@end

void agentLoadPopoverConfigureDashboard(const char *url) {
    NSString *u = (url != NULL) ? [NSString stringWithUTF8String:url] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        [[AgentLoadMenubarPopover shared] configureDashboard:u];
    });
}

void agentLoadPopoverShow(const char *url) {
    NSString *u = (url != NULL) ? [NSString stringWithUTF8String:url] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        [[AgentLoadMenubarPopover shared] showURL:u];
    });
}

void agentLoadPopoverInstallStatusClickFallback(const char *url) {
    NSString *u = (url != NULL) ? [NSString stringWithUTF8String:url] : @"";
    dispatch_async(dispatch_get_main_queue(), ^{
        [[AgentLoadMenubarPopover shared] installStatusClickFallback:u];
    });
}

void agentLoadPopoverHide(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [[AgentLoadMenubarPopover shared] hide];
    });
}

int agentLoadPopoverIsSupported(void) {
    Class wk = NSClassFromString(@"WKWebView");
    return wk != nil ? 1 : 0;
}
