package main

// browser_panel_app.go — Wails bindings for the cowork dock's interactive
// browser panel (the live-tier companion of the mirror): panel visibility
// flow control, pointer/keyboard input forwarding, toolbar navigation, and
// the tab strip. All calls delegate to the kernel's browserpanel.go — the
// desktop layer is deliberately thin (validate + forward), mirroring how
// browser_console_app.go wraps the console session.

import (
	"github.com/zzycxz/hiq/internal/tool/builtin"
)

// BrowserPanelSetVisible turns live screencast streaming on/off for all
// browser sessions. The panel calls it on mount (dock tab active) / unmount.
func (a *App) BrowserPanelSetVisible(visible bool) {
	builtin.SetPanelStreaming(visible)
}

// BrowserPanelDispatch forwards one pointer/keyboard action to the session's
// current tab (coordinates arrive frame-normalized; kernel maps to CSS px).
func (a *App) BrowserPanelDispatch(sessionID string, ev builtin.PanelInputEvent) error {
	return builtin.PanelDispatchInput(sessionID, ev)
}

// BrowserPanelNavigate drives the address bar.
func (a *App) BrowserPanelNavigate(sessionID string, url string) error {
	return builtin.PanelNavigate(sessionID, url)
}

// BrowserPanelBack / BrowserPanelForward / BrowserPanelReload drive the
// toolbar buttons.
func (a *App) BrowserPanelBack(sessionID string) error {
	return builtin.PanelBack(sessionID)
}

func (a *App) BrowserPanelForward(sessionID string) error {
	return builtin.PanelForward(sessionID)
}

func (a *App) BrowserPanelReload(sessionID string) error {
	return builtin.PanelReload(sessionID)
}

// BrowserPanelTabs lists the session's page targets for the tab strip.
func (a *App) BrowserPanelTabs(sessionID string) ([]builtin.PanelTab, error) {
	return builtin.PanelTabs(sessionID)
}

// BrowserPanelSwitchTab moves the session (and the live stream) to the given
// target id.
func (a *App) BrowserPanelSwitchTab(sessionID string, tabID string) error {
	return builtin.PanelSwitchTab(sessionID, tabID)
}

// BrowserPanelStartPick arms the element picker: the page highlights hovered
// elements and the next click reports a descriptor (panel → chip).
func (a *App) BrowserPanelStartPick(sessionID string) error {
	return builtin.PanelStartPick(sessionID)
}

// BrowserPanelStopPick cancels an armed element picker.
func (a *App) BrowserPanelStopPick(sessionID string) error {
	return builtin.PanelStopPick(sessionID)
}

// BrowserPanelTakePick returns and clears the latest picked element, if any.
func (a *App) BrowserPanelTakePick(sessionID string) (*builtin.PickDescriptor, error) {
	return builtin.PanelTakePick(sessionID)
}
