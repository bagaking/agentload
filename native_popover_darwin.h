// native_popover_darwin.h declares the C entry points used by the Go cgo
// bindings to drive the NSPanel + WKWebView popover.

#ifndef AGENTLOAD_NATIVE_POPOVER_H
#define AGENTLOAD_NATIVE_POPOVER_H

#ifdef __cplusplus
extern "C" {
#endif

// agentLoadPopoverConfigureDashboard registers the local dashboard URL the
// popover should hand to NSWorkspace when the embedded HTML asks the host to
// open the browser dashboard. Pass an empty string to disable the action.
void agentLoadPopoverConfigureDashboard(const char *url);

// agentLoadPopoverShow displays the popover and loads the supplied URL. If the
// popover is already visible, it is hidden instead, so the same call
// implements click-to-toggle behavior.
void agentLoadPopoverShow(const char *url);

// agentLoadPopoverInstallStatusClickFallback installs a native click monitor that
// toggles the popover when Control Center status-item replicas do not invoke
// the systray tap callback. Pass an empty URL to remove the monitor.
void agentLoadPopoverInstallStatusClickFallback(const char *url);

// agentLoadPopoverHide hides the popover if it is visible.
void agentLoadPopoverHide(void);

// agentLoadPopoverIsSupported reports whether WKWebView is available on the
// current macOS version. Returns 1 on success, 0 otherwise.
int agentLoadPopoverIsSupported(void);

#ifdef __cplusplus
}
#endif

#endif
