// internal/ui/mode_handlers_helpers_test.go
//
// Test-only method shims for the per-mode key handlers that
// migrated from `func (a *App) handleXxxMode(...)` on App to
// free `func handleXxxMode(a *App, ...)` in mode_*.go during
// Phase 5 of the SOLID refactor.
//
// The shims preserve the pre-Phase-5 test API
// (one-line `app.handleXxxMode(msg)`) without polluting
// production code -- the `_test.go` suffix keeps these methods
// invisible outside the test binary. Mirrors the
// services_helpers_test.go pattern from Phase 3.
//
// Only shims for handlers with test call sites are listed below.
// Phase 5b/5c/5d/5e/5f/5g extractions did not need shims because
// no tests referenced those handlers directly.
package ui

import tea "charm.land/bubbletea/v2"

func (a *App) handleChannelFinderMode(msg tea.KeyMsg) tea.Cmd {
	return handleChannelFinderMode(a, msg)
}
