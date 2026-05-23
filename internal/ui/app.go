// internal/ui/app.go
package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"log"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.design/x/clipboard"
	"github.com/gammons/slk/internal/cache"
	"github.com/gammons/slk/internal/config"
	"github.com/gammons/slk/internal/debuglog"
	"github.com/gammons/slk/internal/emoji"
	imgpkg "github.com/gammons/slk/internal/image"
	"github.com/gammons/slk/internal/ui/channelfinder"
	"github.com/gammons/slk/internal/ui/channelpicker"
	"github.com/gammons/slk/internal/ui/compose"
	"github.com/gammons/slk/internal/ui/confirmprompt"
	"github.com/gammons/slk/internal/ui/help"
	"github.com/gammons/slk/internal/ui/imgrender"
	"github.com/gammons/slk/internal/ui/mentionpicker"
	"github.com/gammons/slk/internal/ui/messages"
	"github.com/gammons/slk/internal/ui/presencemenu"
	"github.com/gammons/slk/internal/ui/reactionpicker"
	"github.com/gammons/slk/internal/ui/sidebar"
	"github.com/gammons/slk/internal/ui/statusbar"
	"github.com/gammons/slk/internal/ui/styles"
	"github.com/gammons/slk/internal/ui/themeswitcher"
	"github.com/gammons/slk/internal/ui/thread"
	"github.com/gammons/slk/internal/ui/threadsview"
	"github.com/gammons/slk/internal/ui/workspace"
	"github.com/gammons/slk/internal/ui/workspacefinder"
)

type Panel int

const (
	PanelWorkspace Panel = iota
	PanelSidebar
	PanelMessages
	PanelThread
)

// View identifies which "page" the message pane is displaying. The default
// is ViewChannels (a channel's message history); ViewThreads swaps the
// pane's contents for the involved-threads list.
type View int

const (
	ViewChannels View = iota
	ViewThreads
)

const (
	// cacheFreshThreshold: cache rendered as-is, no network fetch, no
	// syncing indicator. Channel was visited or WS-updated within this
	// window so the SQLite snapshot is provably recent.
	cacheFreshThreshold = 30 * time.Second
)

// openThreadDebounceDelay is how long openSelectedThreadCmd waits after a
// j/k key event before firing the conversations.replies HTTP call. Held-key
// bursts coalesce into a single fetch against whichever row the cursor
// finally lands on.
const openThreadDebounceDelay = 200 * time.Millisecond

type App struct {
	// Sub-models
	workspaceRail   workspace.Model
	sidebar         sidebar.Model
	messagepane     messages.Model
	compose         compose.Model
	statusbar       statusbar.Model
	channelFinder   channelfinder.Model
	workspaceFinder workspacefinder.Model
	themeSwitcher   themeswitcher.Model
	presenceMenu    presencemenu.Model
	help            help.Model
	threadPanel     *thread.Model
	threadCompose   compose.Model
	threadsView     threadsview.Model

	// State
	mode           Mode
	focusedPanel   Panel
	sidebarVisible bool
	threadVisible  bool
	view           View
	width          int
	height         int
	keys           KeyMap

	// layout owns the per-frame layout geometry (horizontal bands for
	// mouse hit-testing + per-pane content heights for pageSize). See
	// internal/ui/panellayout.go.
	layout *panelLayout

	// renderCache aggregates the six per-panel render caches. See
	// internal/ui/panelcache.go.
	renderCache *panelRenderCache

	// Current context
	activeChannelID string
	activeTeamID    string // workspace whose data is currently loaded into the side panels

	// Callbacks
	// channels is the App's ChannelService collaborator (Slack
	// channels API + local cache + session bookkeeping). See
	// internal/ui/services.go. Defaulted to a no-op adapter in
	// NewApp so call sites can dispatch without nil-checks.
	channels ChannelService
	// messages is the App's MessageService collaborator (send / edit /
	// delete / mark-unread / permalink). See internal/ui/services.go.
	// Defaulted to a no-op adapter in NewApp so call sites can dispatch
	// without nil-checks.
	messageSvc MessageService

	uploader UploadFunc

	// clipboardAvailable is set at startup based on the result of
	// clipboard.Init(). When false, Ctrl+V smart-paste is a no-op.
	clipboardAvailable bool

	// clipboardRead is the function used by smartPaste to read OS
	// clipboard contents. Tests inject fakes via SetClipboardReader.
	clipboardRead clipboardReader

	// threads is the App's ThreadService collaborator (fetch / mark /
	// reply / list-fetch + parent-channel last-read lookup for the
	// unread boundary). See internal/ui/services.go. Defaulted to a
	// no-op adapter in NewApp so call sites can dispatch without
	// nil-checks.
	threads ThreadService

	threadsDirtyDebounce time.Duration
	fetchingOlder        bool

	// Cached user-id -> display-name map (mirror of what SetUserNames
	// last received). Used by openSelectedThreadCmd to populate the
	// thread panel parent's UserName without round-tripping through any
	// sub-component's API.
	userNames map[string]string

	// externalUsers tracks which user IDs are Slack Connect / shared-channel
	// guests. Populated by main.go via SetExternalUsers as users are
	// resolved. Read by SetUserNames when building the mention-picker User
	// slice so IsExternal is set on each entry.
	externalUsers map[string]bool
	// Last (channelID, threadTS) auto-opened from the threads view.
	// openSelectedThreadCmd compares against these to dedup repeat calls
	// (j/k keystrokes and ThreadsListLoadedMsg refreshes both fire
	// openSelectedThreadCmd; without dedup we'd hammer the Slack API and
	// clobber the right thread panel mid-read). Cleared whenever the
	// user leaves the threads view (ChannelSelectedMsg, CloseThread,
	// workspace switch).
	lastOpenedChannelID string
	lastOpenedThreadTS  string

	// pendingThreadFetchGen is bumped by every debounced openSelectedThreadCmd
	// call (j/k path). The threadFetchDebounceMsg handler only runs the network
	// fetch when its `gen` matches; older ticks are dropped so a held j produces
	// exactly one fetch (for the row the user finally lands on). Non-debounced
	// callers (activation, list reload, G jump) do NOT bump this — bumping there
	// would needlessly invalidate any in-flight debounced fetch about to land.
	pendingThreadFetchGen uint64

	// Reaction picker
	reactionPicker   *reactionpicker.Model
	confirmPrompt    *confirmprompt.Model
	// reactions is the App's ReactionService collaborator (add/remove
	// reactions on Slack + load/record frecent emoji history). See
	// internal/ui/services.go. Defaulted to a no-op adapter in NewApp
	// so call sites can dispatch without nil-checks.
	reactions ReactionService
	currentUserID    string

	// editing tracks in-progress message edit state. See
	// internal/ui/edit.go.
	editing *editController

	// Workspace switching
	workspaceSwitcher SwitchWorkspaceFunc
	workspaceItems    []workspace.WorkspaceItem // cached for lookup
	// lastChannelByTeam remembers the active channel ID per workspace so
	// that switching back to a workspace returns to the same channel the
	// user was last viewing there. Saved at the start of every workspace
	// switch (the workspace being left), consulted when applying the
	// switch (the workspace being entered). Falls back to the first
	// channel in the sidebar when there is no saved entry or the saved
	// channel is no longer in the list.
	lastChannelByTeam map[string]string

	// navHistory owns the per-workspace ctrl+h / ctrl+k browser-style
	// jump list. See internal/ui/navhistory.go. Lazy-initialized on
	// first push for each team. Cleared only when slk exits — the
	// stacks are session-only by design.
	navHistory *navHistoryStore

	// Theme switching
	themeSaveFn    func(name string, scope themeswitcher.ThemeScope)
	themeOverrides config.Theme

	// presence owns per-workspace presence/DND cache, the DND-tick
	// guard, and the custom-snooze numeric input buffer. See
	// internal/ui/presence.go.
	presence *presenceController
	// setStatusFn is the callback invoked when the user picks a presence-
	// menu action; it runs the Slack API call for the active workspace.
	// Wired by cmd/slk/main.go via SetStatusSetter.
	setStatusFn func(action presencemenu.Action, snoozeMinutes int)

	// typing owns both inbound typing-indicator state (other users
	// typing in channels) and outbound typing-send throttle. See
	// internal/ui/typing.go.
	typing    *typingTracker
	typingOut *typingBroadcaster

	// mouseWheelLines is the number of lines the viewport scrolls per
	// mouse-wheel notch. Plumbed from [appearance].mouse_wheel_lines.
	// Falls back to 3 when unset (matches the pre-config behavior).
	mouseWheelLines int

	// selfSend tracks slk-originated message sends so WS echoes can
	// be suppressed (preventing the visible flicker between Slack's
	// normalised echo text and the optimistic instant-display text).
	// See internal/ui/selfsend.go. Used for both channel-level
	// in-flight suppression (before chat.postMessage's response
	// returns) and TS-level exact-match suppression (after).
	selfSend *selfSendDedup

	// nowTimestampFormatter renders "now" using the same format used
	// for message timestamps elsewhere (configured via
	// cfg.Appearance.TimestampFormat in main.go). Used by the optimistic
	// instant-display path to populate the Timestamp field of a
	// placeholder MessageItem so it renders identically to messages
	// that arrived via the normal HTTP-response / WS-echo paths.
	// Falls back to time.Now().Format("3:04 PM") when unset.
	nowTimestampFormatter func() string

	// bootstrap owns the multi-workspace startup overlay (loading flag,
	// per-workspace status entries, initial-active claim guard, and the
	// overlay's render). See internal/ui/bootstrap.go.
	bootstrap *workspaceBootstrap

	// spinnerFrame is the global tick counter for the loading spinner
	// glyph. Shared by the bootstrap overlay and the messages pane's
	// in-channel load spinner; advanced by SpinnerTickMsg.
	spinnerFrame int

	// drag owns the mouse-drag selection FSM (set by MouseClickMsg,
	// advanced by MouseMotionMsg, drained by MouseReleaseMsg). See
	// internal/ui/drag.go.
	drag *dragState

	// imageFetcher is the inline-image fetcher shared with the messages
	// pane; the App uses it to load the larger thumb when the user
	// opens the full-screen preview overlay. Wired via SetImageFetcher
	// from main.go, after Detect / cache construction.
	imageFetcher *imgpkg.Fetcher

	// imgProtocol is the active terminal image protocol detected at
	// startup. Used to render the full-screen preview overlay.
	imgProtocol imgpkg.Protocol

	// preview owns the full-screen image preview overlay state
	// (overlay + the channel/ts/attIdx triple that lets h/l/arrow
	// cycling locate sibling attachments). See internal/ui/imagepreview.go.
	preview *imagePreviewController
}

func NewApp() *App {
	app := &App{
		workspaceRail:        workspace.New(nil, 0),
		sidebar:              sidebar.New(nil),
		messagepane:          messages.New(nil, ""),
		compose:              compose.New(""),
		statusbar:            statusbar.New(),
		channelFinder:        channelfinder.New(),
		workspaceFinder:      workspacefinder.New(),
		themeSwitcher:        themeswitcher.New(),
		presenceMenu:         presencemenu.New(),
		help:                 help.New(),
		threadPanel:          thread.New(),
		threadCompose:        compose.New("thread"),
		threadsView:          threadsview.New(nil, ""),
		reactionPicker:       reactionpicker.New(),
		confirmPrompt:        confirmprompt.New(),
		mode:                 ModeNormal,
		focusedPanel:         PanelSidebar,
		sidebarVisible:       true,
		view:                 ViewChannels,
		keys:                 DefaultKeyMap(),
		selfSend:             newSelfSendDedup(),
		bootstrap:            newWorkspaceBootstrap(),
		threadsDirtyDebounce: 150 * time.Millisecond,
		mouseWheelLines:      3,
		userNames:            map[string]string{},
		externalUsers:        map[string]bool{},
		presence:             newPresenceController(),
		renderCache:          newPanelRenderCache(),
		drag:                 newDragState(),
		preview:              newImagePreviewController(),
		layout:               newPanelLayout(),
		reactions:            noopReactionService,
		threads:              noopThreadService,
		messageSvc:           noopMessageService,
		channels:             noopChannelService,
		lastChannelByTeam:    map[string]string{},
		navHistory:           newNavHistoryStore(),
		clipboardRead:        defaultClipboardReader,
	}
	app.editing = newEditController()
	// typing tracker is referenced by typingOut so it must exist first;
	// construct outside the literal because struct literals can't
	// reference sibling fields.
	app.typing = newTypingTracker()
	app.typingOut = newTypingBroadcaster(app.typing)
	// Seed the picker with built-in emojis so the autocomplete works even
	// before the first workspace finishes loading customs.
	app.compose.SetEmojiEntries(emoji.BuildEntries(nil))
	app.threadCompose.SetEmojiEntries(emoji.BuildEntries(nil))
	// Seed the channel finder with the "Threads" view shortcut so users
	// can jump to the threads-list view from the same overlay they use
	// to switch channels (ctrl+t / ctrl+p). Selecting this row dispatches
	// ThreadsViewActivatedMsg in handleChannelFinderMode below.
	app.channelFinder.SetSyntheticItems([]channelfinder.Item{{
		ID:     channelfinder.ThreadsViewID,
		Name:   "Threads",
		Type:   "threads",
		Joined: true,
	}})
	// Seed the statusbar hint with the configured help key label so it
	// stays accurate if the binding is ever changed.
	if helpKey := app.keys.Help.Help().Key; helpKey != "" {
		app.statusbar.SetHelpHint(helpKey + " for keybindings")
	}
	return app
}

func (a *App) Init() tea.Cmd {
	if a.bootstrap.IsLoading() {
		return tea.Batch(
			tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
				return SpinnerTickMsg{}
			}),
			tea.Tick(15*time.Second, func(time.Time) tea.Msg {
				return LoadingTimeoutMsg{}
			}),
		)
	}
	return nil
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Phase 4 reducer chain (extension point — see internal/ui/reducers.go).
	// Each reducer owns a cohesive family of message types and either
	// claims the message (returning its cmd, true) or passes (nil,
	// false). The first claimant short-circuits the rest of Update;
	// unclaimed messages fall through to the residual switch below.
	// Reducers are added to this chain one family at a time as the
	// switch shrinks. Order doesn't matter semantically — message
	// types are disjoint across reducers — but stable order keeps
	// the trace predictable.
	if cmd, handled := dispatchReducers(a, msg,
		a.presence,
		a.preview,
		a.drag,
		a.typing,
		a.bootstrap,
		reduceReactions,
		reduceThreads,
		reduceSend,
		reduceChannels,
		reduceWorkspace,
		reduceIO,
		reduceMouse,
	); handled {
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)
	}

	// While the full-screen image preview overlay is open, route close
	// and cycle keys directly:
	//   - Esc/q: dismiss
	//   - Enter: dismiss + launch OS image viewer
	//   - h or left arrow: previous sibling image (wraps)
	//   - l or right arrow: next sibling image (wraps)
	// All other keys are swallowed so navigation in the messages pane
	// doesn't leak through. Resize / mouse / async messages still flow
	// normally so the rest of the UI keeps ticking — including the
	// previewLoadedMsg arm that swaps the cycled image into place.
	if a.preview.Active() {
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "esc", "q":
				a.preview.Close()
				return a, nil
			case "enter":
				path := a.preview.Overlay().Path()
				a.preview.Close()
				return a, openInSystemViewerCmd(path)
			case "h", "left":
				if a.preview.Overlay().SiblingCount() > 1 {
					return a, a.cycleImagePreviewCmd(a.preview.Channel(), a.preview.TS(), a.preview.AttIdx(), -1)
				}
				return a, nil
			case "l", "right":
				if a.preview.Overlay().SiblingCount() > 1 {
					return a, a.cycleImagePreviewCmd(a.preview.Channel(), a.preview.TS(), a.preview.AttIdx(), +1)
				}
				return a, nil
			}
			return a, nil
		}
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case tea.KeyMsg:
		cmd := a.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	// All non-WindowSize, non-Key message types are owned by Phase 4
	// reducers; see the dispatch chain at the top of Update and the
	// per-family reducer_*.go files / controller Handle methods.
	}

	return a, tea.Batch(cmds...)
}

func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Ctrl+C is intercepted globally and routed through the same
	// confirm prompt as lowercase `q`, so an accidental Ctrl+C while
	// reading or typing doesn't yank the whole app out from under the
	// user. `Q` (capital) remains the no-prompt force-quit, and an
	// already-open quit prompt isn't reopened (Enter confirms, Esc
	// cancels via the existing confirm-mode handler).
	if key.Matches(msg, a.keys.Quit) {
		if a.mode != ModeConfirm {
			a.openQuitConfirm()
		}
		return nil
	}

	if a.bootstrap.IsLoading() {
		return nil
	}

	// Mode-specific handling. Dispatch table lives in
	// mode_handlers.go; unmapped modes fall back to Normal
	// (mirrors the pre-Phase-5 `default:` arm).
	return dispatchModeKey(a, msg)
}

// navigateBack walks the per-workspace history stack one step
// backward, skipping any channel IDs that no longer resolve via
// channelLookup (and dropping them from the stack). Returns a tea.Cmd
// that synthesizes a ChannelSelectedMsg{FromHistory: true} for the
// new target, or nil if there's no valid earlier entry.
func (a *App) navigateBack() tea.Cmd {
	return a.walkNavCmd(-1)
}

// navigateForward is the symmetric opposite of navigateBack.
func (a *App) navigateForward() tea.Cmd {
	return a.walkNavCmd(+1)
}

// walkNavCmd is the shared wrapper that turns navHistoryStore.Walk's
// pure result into the ChannelSelectedMsg{FromHistory: true} tea.Cmd
// the App emits. step must be -1 or +1.
func (a *App) walkNavCmd(step int) tea.Cmd {
	id, name, ctype, ok := a.navHistory.Walk(a.activeTeamID, step, a.channels.Lookup)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		return ChannelSelectedMsg{ID: id, Name: name, Type: ctype, FromHistory: true}
	}
}

func (a *App) handleNormalMode(msg tea.KeyMsg) tea.Cmd {
	// Reaction-nav sub-state (intercept before normal keys)
	if a.focusedPanel == PanelMessages && a.messagepane.ReactionNavActive() {
		return a.handleReactionNav(msg)
	}
	if a.focusedPanel == PanelThread && a.threadPanel.ReactionNavActive() {
		return a.handleThreadReactionNav(msg)
	}

	switch {
	case key.Matches(msg, a.keys.InsertMode):
		a.SetMode(ModeInsert)
		// In the Threads view there is no main compose box — the only
		// way to type is into the right-side thread panel's compose.
		// Force focus there even when the threads list itself was the
		// focused panel.
		if a.focusedPanel == PanelThread || (a.view == ViewThreads && a.threadVisible) {
			a.focusedPanel = PanelThread
			return a.threadCompose.Focus()
		}
		a.focusedPanel = PanelMessages
		return a.compose.Focus()

	case key.Matches(msg, a.keys.Escape):
		a.cancelEdit()
		a.SetMode(ModeNormal)
		a.compose.Blur()
		if a.threadVisible {
			a.CloseThread()
		}

	case key.Matches(msg, a.keys.Tab):
		a.FocusNext()

	case key.Matches(msg, a.keys.ShiftTab):
		a.FocusPrev()

	case key.Matches(msg, a.keys.ToggleSidebar):
		a.ToggleSidebar()

	case key.Matches(msg, a.keys.ToggleThread):
		a.ToggleThread()

	case key.Matches(msg, a.keys.NavBack):
		if cmd := a.navigateBack(); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.NavForward):
		if cmd := a.navigateForward(); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.Down):
		if cmd := a.handleDown(); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.Up):
		if cmd := a.handleUp(); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.Left):
		a.FocusPrev()

	case key.Matches(msg, a.keys.Right):
		a.FocusNext()

	case key.Matches(msg, a.keys.Enter):
		return a.handleEnter()

	case key.Matches(msg, a.keys.ToggleSection):
		// Space on a sidebar section header toggles its collapsed
		// state; elsewhere it falls through to whatever the focused
		// panel does with a literal space (typically nothing in
		// normal mode).
		if a.focusedPanel == PanelSidebar {
			if a.sidebar.ToggleCollapseSelected() {
				return nil
			}
		}

	case key.Matches(msg, a.keys.Bottom):
		if cmd := a.handleGoToBottom(); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.PageUp):
		if cmd := a.scrollFocusedPanel(-a.pageSize()); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.PageDown):
		if cmd := a.scrollFocusedPanel(a.pageSize()); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.HalfPageUp):
		if cmd := a.scrollFocusedPanel(-a.halfPageSize()); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.HalfPageDown):
		if cmd := a.scrollFocusedPanel(a.halfPageSize()); cmd != nil {
			return cmd
		}

	case key.Matches(msg, a.keys.Help):
		a.help.SetEntries(help.FromKeyMap(a.keys))
		a.help.Open()
		a.SetMode(ModeHelp)

	case key.Matches(msg, a.keys.WorkspaceFinder):
		a.workspaceFinder.Open()
		a.SetMode(ModeWorkspaceFinder)

	case key.Matches(msg, a.keys.ThemeSwitcher):
		// Per-workspace scope. Header text shows the current workspace name.
		header := "Theme for " + a.activeTeamName()
		a.themeSwitcher.OpenWithScope(themeswitcher.ScopeWorkspace, header)
		a.SetMode(ModeThemeSwitcher)
		return nil
	case key.Matches(msg, a.keys.ThemeSwitcherGlobal):
		a.themeSwitcher.OpenWithScope(themeswitcher.ScopeGlobal, "Default theme for new workspaces")
		a.SetMode(ModeThemeSwitcher)
		return nil

	case key.Matches(msg, a.keys.PresenceMenu):
		header := a.workspaceNameForActive()
		pres, dndEnabled, dndEnd, _ := a.presence.Status(a.activeTeamID)
		a.presenceMenu.OpenWith(header, pres, dndEnabled, dndEnd)
		a.SetMode(ModePresenceMenu)

	case key.Matches(msg, a.keys.FuzzyFinder) || key.Matches(msg, a.keys.FuzzyFinderAlt):
		a.channelFinder.Open()
		a.SetMode(ModeChannelFinder)

	case key.Matches(msg, a.keys.Reaction):
		if a.focusedPanel == PanelMessages {
			return a.openPickerFromMessage()
		} else if a.focusedPanel == PanelThread {
			return a.openPickerFromThread()
		}

	case key.Matches(msg, a.keys.ReactionNav):
		if a.focusedPanel == PanelMessages {
			a.messagepane.EnterReactionNav()
		} else if a.focusedPanel == PanelThread {
			a.threadPanel.EnterReactionNav()
		}

	case key.Matches(msg, a.keys.CopyPermalink):
		return a.copyPermalinkOfSelected()

	case key.Matches(msg, a.keys.Edit):
		return a.beginEditOfSelected()

	case key.Matches(msg, a.keys.Delete):
		return a.beginDeleteOfSelected()

	case key.Matches(msg, a.keys.OpenPreview):
		return a.openImagePreviewOfSelected()

	case key.Matches(msg, a.keys.MarkUnread):
		return a.markUnreadOfSelected()

	case key.Matches(msg, a.keys.CloseThreadView):
		// Lowercase q is "close thread view" when one is open; if no
		// thread panel is visible it's a no-op (Q and Ctrl+C are the
		// quit keys). The vim-style pairing: q closes the transient
		// pane, Q closes the whole app.
		if a.threadVisible {
			a.CloseThread()
		}
		return nil

	case key.Matches(msg, a.keys.QuitConfirm):
		a.openQuitConfirm()
		return nil

	default:
		// Number keys 1-9 switch workspaces
		keyStr := msg.String()
		if len(keyStr) == 1 && keyStr[0] >= '1' && keyStr[0] <= '9' {
			idx := int(keyStr[0] - '1') // 0-indexed
			if idx < len(a.workspaceItems) && a.workspaceSwitcher != nil {
				if a.workspaceItems[idx].ID != a.workspaceRail.SelectedID() {
					switcher := a.workspaceSwitcher
					teamID := a.workspaceItems[idx].ID
					return func() tea.Msg {
						return switcher(teamID)
					}
				}
			}
		}
	}
	return nil
}

func (a *App) handleInsertMode(msg tea.KeyMsg) tea.Cmd {
	if (a.compose.Uploading() || a.threadCompose.Uploading()) && key.Matches(msg, a.keys.Escape) {
		return a.uploadToastCmd("Upload in progress", 2*time.Second)
	}
	if a.editing.IsActive() && key.Matches(msg, a.keys.Escape) {
		// If a picker is active in the relevant compose, close it
		// instead of cancelling the edit.
		if a.editing.Panel() == PanelThread {
			if a.threadCompose.IsEmojiActive() {
				a.threadCompose.CloseEmoji()
				return nil
			}
			if a.threadCompose.IsMentionActive() {
				a.threadCompose.CloseMention()
				return nil
			}
			if a.threadCompose.IsChannelActive() {
				a.threadCompose.CloseChannel()
				return nil
			}
		} else {
			if a.compose.IsEmojiActive() {
				a.compose.CloseEmoji()
				return nil
			}
			if a.compose.IsMentionActive() {
				a.compose.CloseMention()
				return nil
			}
			if a.compose.IsChannelActive() {
				a.compose.CloseChannel()
				return nil
			}
		}
		a.cancelEdit()
		return nil
	}
	if key.Matches(msg, a.keys.Escape) {
		// If a picker is active, close it instead of exiting insert mode.
		if a.focusedPanel == PanelThread && a.threadVisible {
			if a.threadCompose.IsEmojiActive() {
				a.threadCompose.CloseEmoji()
				return nil
			}
			if a.threadCompose.IsMentionActive() {
				a.threadCompose.CloseMention()
				return nil
			}
			if a.threadCompose.IsChannelActive() {
				a.threadCompose.CloseChannel()
				return nil
			}
		} else {
			if a.compose.IsEmojiActive() {
				a.compose.CloseEmoji()
				return nil
			}
			if a.compose.IsMentionActive() {
				a.compose.CloseMention()
				return nil
			}
			if a.compose.IsChannelActive() {
				a.compose.CloseChannel()
				return nil
			}
		}
		a.SetMode(ModeNormal)
		a.compose.Blur()
		a.threadCompose.Blur()
		return nil
	}

	code := msg.Key().Code
	mod := msg.Key().Mod
	isPaste := code == 'v' && mod == tea.ModCtrl
	if isPaste {
		return a.smartPaste()
	}

	// Insert-mode shortcuts that operate on the active compose:
	//   Ctrl+U  → clear compose (text + attachments + uploading flag)
	//   Up      → if cursor on first line, jump to start of textarea
	//   Down    → if cursor on last line,  jump to end of textarea
	target := &a.compose
	if a.focusedPanel == PanelThread && a.threadVisible {
		target = &a.threadCompose
	}
	if code == 'u' && mod == tea.ModCtrl {
		target.Reset()
		return nil
	}
	// If a compose-overlay picker (emoji / @mention / #channel) is active,
	// let it own Up/Down so users can navigate the suggestion list. Without
	// this guard, the jump-to-start/end shortcuts below swallow the arrow
	// keys before the picker ever sees them.
	pickerActive := target.IsEmojiActive() || target.IsMentionActive() || target.IsChannelActive()
	if !pickerActive {
		if code == tea.KeyUp && mod == 0 && target.CursorAtFirstLine() {
			target.MoveCursorToStart()
			return nil
		}
		if code == tea.KeyDown && mod == 0 && target.CursorAtLastLine() {
			target.MoveCursorToEnd()
			return nil
		}
	}
	// Plain Enter sends; Shift+Enter (and Ctrl+J as a fallback for terminals
	// that don't disambiguate modifiers) inserts a newline.
	isSend := code == tea.KeyEnter && !mod.Contains(tea.ModShift)
	isNewline := (code == tea.KeyEnter && mod.Contains(tea.ModShift)) ||
		(code == 'j' && mod == tea.ModCtrl)

	// Determine which compose box is active based on focused panel
	if a.focusedPanel == PanelThread && a.threadVisible {
		// If a picker is active, forward all keys to compose (including Enter).
		if a.threadCompose.IsEmojiActive() || a.threadCompose.IsMentionActive() || a.threadCompose.IsChannelActive() {
			var cmd tea.Cmd
			a.threadCompose, cmd = a.threadCompose.Update(msg)
			return cmd
		}

		// Thread reply compose
		if isNewline {
			var cmd tea.Cmd
			a.threadCompose, cmd = a.threadCompose.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			return cmd
		}
		if isSend {
			if len(a.threadCompose.Attachments()) > 0 {
				cmd := a.submitWithAttachments(&a.threadCompose)
				if a.threadCompose.Uploading() {
					a.exitInsertAfterSend()
				}
				return cmd
			}
			if a.editing.IsActive() && a.editing.Panel() == PanelThread {
				return a.submitEdit(a.threadCompose.Value(), a.threadCompose.TranslateMentionsForSend(a.threadCompose.Value()))
			}
			text := a.threadCompose.Value()
			if text != "" {
				text = a.threadCompose.TranslateMentionsForSend(text)
				a.threadCompose.Reset()
				threadTS := a.threadPanel.ThreadTS()
				channelID := a.threadPanel.ChannelID()
				a.exitInsertAfterSend()
				return func() tea.Msg {
					return SendThreadReplyMsg{
						ChannelID: channelID,
						ThreadTS:  threadTS,
						Text:      text,
					}
				}
			}
			return nil
		}
		var cmd tea.Cmd
		a.threadCompose, cmd = a.threadCompose.Update(msg)
		a.typingOut.MaybeSend(a.threadPanel.ChannelID())
		return cmd
	}

	// Channel message compose
	// If a picker is active, forward all keys to compose (including Enter).
	if a.compose.IsEmojiActive() || a.compose.IsMentionActive() || a.compose.IsChannelActive() {
		var cmd tea.Cmd
		a.compose, cmd = a.compose.Update(msg)
		return cmd
	}

	if isNewline {
		var cmd tea.Cmd
		a.compose, cmd = a.compose.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return cmd
	}
	if isSend {
		if len(a.compose.Attachments()) > 0 {
			cmd := a.submitWithAttachments(&a.compose)
			if a.compose.Uploading() {
				a.exitInsertAfterSend()
			}
			return cmd
		}
		if a.editing.IsActive() && a.editing.Panel() == PanelMessages {
			return a.submitEdit(a.compose.Value(), a.compose.TranslateMentionsForSend(a.compose.Value()))
		}
		text := a.compose.Value()
		if text != "" {
			text = a.compose.TranslateMentionsForSend(text)
			a.compose.Reset()
			a.exitInsertAfterSend()
			return func() tea.Msg {
				return SendMessageMsg{
					ChannelID: a.activeChannelID,
					Text:      text,
				}
			}
		}
		return nil
	}

	var cmd tea.Cmd
	a.compose, cmd = a.compose.Update(msg)
	a.typingOut.MaybeSend(a.activeChannelID)
	return cmd
}

func (a *App) handleCommandMode(msg tea.KeyMsg) tea.Cmd {
	if key.Matches(msg, a.keys.Escape) {
		a.SetMode(ModeNormal)
	}
	return nil
}

func (a *App) handleChannelFinderMode(msg tea.KeyMsg) tea.Cmd {
	// Map tea.KeyMsg to string for the finder
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	result := a.channelFinder.HandleKey(keyStr)
	if result != nil {
		a.channelFinder.Close()
		a.SetMode(ModeNormal)
		// Synthetic destinations (e.g. Threads view) live alongside
		// channels in the finder but route to a view activation rather
		// than a channel switch.
		if result.Type == "threads" {
			return func() tea.Msg { return ThreadsViewActivatedMsg{} }
		}
		// Already-joined: switch immediately. Not joined: kick off a join
		// command; ChannelJoinedMsg will fold the channel into the sidebar
		// and switch to it.
		if result.Joined {
			a.sidebar.SelectByID(result.ID)
			return func() tea.Msg {
				return ChannelSelectedMsg{ID: result.ID, Name: result.Name, Type: result.Type}
			}
		}
		channels := a.channels
		id, name := result.ID, result.Name
		return func() tea.Msg {
			return channels.Join(id, name)
		}
	}

	// Check if finder closed itself (Esc)
	if !a.channelFinder.IsVisible() {
		a.SetMode(ModeNormal)
	}

	return nil
}

func (a *App) handleWorkspaceFinderMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	result := a.workspaceFinder.HandleKey(keyStr)
	if result != nil {
		a.workspaceFinder.Close()
		a.SetMode(ModeNormal)
		if a.workspaceSwitcher != nil && result.ID != a.workspaceRail.SelectedID() {
			switcher := a.workspaceSwitcher
			teamID := result.ID
			return func() tea.Msg {
				return switcher(teamID)
			}
		}
	}
	if !a.workspaceFinder.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return nil
}

func (a *App) handleThemeSwitcherMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	result := a.themeSwitcher.HandleKey(keyStr)
	if result != nil {
		a.themeSwitcher.Close()
		a.SetMode(ModeNormal)
		// Apply theme immediately
		styles.Apply(result.Name, a.themeOverrides)
		// Invalidate render caches so they rebuild with new theme colors
		a.messagepane.InvalidateCache()
		a.threadPanel.InvalidateCache()
		a.sidebar.InvalidateCache()
		// Refresh compose textarea styles for new theme
		a.compose.RefreshStyles()
		a.threadCompose.RefreshStyles()
		// Save selection
		if a.themeSaveFn != nil {
			a.themeSaveFn(result.Name, result.Scope)
		}
		return nil
	}
	if !a.themeSwitcher.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return nil
}

// handleHelpMode dispatches key events to the help overlay and tears down
// the mode when the overlay closes itself (esc/q/?).
func (a *App) handleHelpMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}
	a.help.HandleKey(keyStr)
	if !a.help.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return nil
}

func (a *App) handlePresenceMenuMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	result := a.presenceMenu.HandleKey(keyStr)
	if result != nil {
		a.presenceMenu.Close()
		// Custom snooze opens a sub-mode instead of firing immediately.
		if result.Action == presencemenu.ActionCustomSnooze {
			a.presence.ClearSnoozeBuf()
			a.SetMode(ModePresenceCustomSnooze)
			return nil
		}
		a.SetMode(ModeNormal)
		// Optimistic UI: update local state + status bar before the API
		// call returns. The WS echo will reaffirm it.
		st := a.presence.Apply(a.activeTeamID, result.Action, result.SnoozeMinutes)
		a.statusbar.SetStatus(st.Presence, st.DNDEnabled, st.DNDEndTS)
		if a.setStatusFn != nil {
			a.setStatusFn(result.Action, result.SnoozeMinutes)
		}
		return nil
	}
	if !a.presenceMenu.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return nil
}

func (a *App) handlePresenceCustomSnoozeMode(msg tea.KeyMsg) tea.Cmd {
	switch msg.Key().Code {
	case tea.KeyEscape:
		a.presence.ClearSnoozeBuf()
		a.SetMode(ModeNormal)
		return nil
	case tea.KeyEnter:
		mins, err := strconv.Atoi(a.presence.SnoozeBuf())
		a.presence.ClearSnoozeBuf()
		a.SetMode(ModeNormal)
		if err != nil || mins <= 0 {
			a.statusbar.SetToast("Invalid snooze duration")
			return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return statusbar.CopiedClearMsg{} })
		}
		st := a.presence.Apply(a.activeTeamID, presencemenu.ActionSnooze, mins)
		a.statusbar.SetStatus(st.Presence, st.DNDEnabled, st.DNDEndTS)
		if a.setStatusFn != nil {
			a.setStatusFn(presencemenu.ActionSnooze, mins)
		}
		return nil
	case tea.KeyBackspace:
		a.presence.BackspaceSnooze()
		return nil
	}
	a.presence.AppendSnoozeDigit(msg.String())
	return nil
}

func (a *App) handleReactionPickerMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()

	switch msg.Key().Code {
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyEnter:
		keyStr = "enter"
	case tea.KeyUp:
		keyStr = "up"
	case tea.KeyDown:
		keyStr = "down"
	case tea.KeyBackspace:
		keyStr = "backspace"
	}

	// Capture values before HandleKey (which may call Close and reset them)
	channelID := a.reactionPicker.ChannelID()
	messageTS := a.reactionPicker.MessageTS()

	result := a.reactionPicker.HandleKey(keyStr)

	if !a.reactionPicker.IsVisible() {
		// Esc was pressed
		a.SetMode(ModeNormal)
		return nil
	}

	if result != nil {
		emojiName := result.Emoji

		a.reactionPicker.Close()
		a.SetMode(ModeNormal)

		// Record frecent usage on add (not remove)
		if !result.Remove {
			a.reactions.RecordFrecent(emojiName)
		}

		// Optimistic update
		a.updateReactionOnMessage(channelID, messageTS, emojiName, a.currentUserID, result.Remove)

		// Fire API call
		if result.Remove {
			return func() tea.Msg {
				err := a.reactions.Remove(channelID, messageTS, emojiName)
				return ReactionSentMsg{Err: err}
			}
		}
		return func() tea.Msg {
			err := a.reactions.Add(channelID, messageTS, emojiName)
			return ReactionSentMsg{Err: err}
		}
	}

	return nil
}

func (a *App) handleConfirmMode(msg tea.KeyMsg) tea.Cmd {
	keyStr := msg.String()
	switch msg.Key().Code {
	case tea.KeyEscape:
		keyStr = "esc"
	case tea.KeyEnter:
		keyStr = "enter"
	}

	res := a.confirmPrompt.HandleKey(keyStr)
	if !a.confirmPrompt.IsVisible() {
		a.SetMode(ModeNormal)
	}
	return res.Cmd
}

func (a *App) updateReactionOnMessage(channelID, messageTS, emojiName, userID string, remove bool) {
	a.messagepane.UpdateReaction(messageTS, emojiName, userID, remove)
	a.threadPanel.UpdateReaction(messageTS, emojiName, userID, remove)
}

func (a *App) handleReactionNav(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, a.keys.Left):
		a.messagepane.ReactionNavLeft()
	case key.Matches(msg, a.keys.Right):
		a.messagepane.ReactionNavRight()
	case key.Matches(msg, a.keys.Enter):
		emojiName, isPlus := a.messagepane.SelectedReaction()
		if isPlus {
			return a.openPickerFromMessage()
		}
		return a.toggleReactionOnSelectedMessage(emojiName)
	case key.Matches(msg, a.keys.Reaction):
		return a.openPickerFromMessage()
	case key.Matches(msg, a.keys.Escape):
		a.messagepane.ExitReactionNav()
	}
	return nil
}

func (a *App) handleThreadReactionNav(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, a.keys.Left):
		a.threadPanel.ReactionNavLeft()
	case key.Matches(msg, a.keys.Right):
		a.threadPanel.ReactionNavRight()
	case key.Matches(msg, a.keys.Enter):
		emojiName, isPlus := a.threadPanel.SelectedReaction()
		if isPlus {
			return a.openPickerFromThread()
		}
		return a.toggleReactionOnSelectedThread(emojiName)
	case key.Matches(msg, a.keys.Reaction):
		return a.openPickerFromThread()
	case key.Matches(msg, a.keys.Escape):
		a.threadPanel.ExitReactionNav()
	}
	return nil
}

func (a *App) openPickerFromMessage() tea.Cmd {
	msg, ok := a.messagepane.SelectedMessage()
	if !ok {
		return nil
	}
	var existing []string
	for _, r := range msg.Reactions {
		if r.HasReacted {
			existing = append(existing, r.Emoji)
		}
	}
	a.messagepane.ExitReactionNav()
	a.reactionPicker.SetFrecentEmoji(a.reactions.LoadFrecent(10))
	a.reactionPicker.Open(a.activeChannelID, msg.TS, existing)
	a.SetMode(ModeReactionPicker)
	return nil
}

func (a *App) openPickerFromThread() tea.Cmd {
	reply := a.threadPanel.SelectedReply()
	if reply == nil {
		return nil
	}
	var existing []string
	for _, r := range reply.Reactions {
		if r.HasReacted {
			existing = append(existing, r.Emoji)
		}
	}
	a.threadPanel.ExitReactionNav()
	a.reactionPicker.SetFrecentEmoji(a.reactions.LoadFrecent(10))
	a.reactionPicker.Open(a.threadPanel.ChannelID(), reply.TS, existing)
	a.SetMode(ModeReactionPicker)
	return nil
}

func (a *App) toggleReactionOnSelectedMessage(emojiName string) tea.Cmd {
	msg, ok := a.messagepane.SelectedMessage()
	if !ok {
		return nil
	}
	remove := false
	for _, r := range msg.Reactions {
		if r.Emoji == emojiName && r.HasReacted {
			remove = true
			break
		}
	}
	a.updateReactionOnMessage(a.activeChannelID, msg.TS, emojiName, a.currentUserID, remove)
	channelID := a.activeChannelID
	ts := msg.TS
	if remove {
		return func() tea.Msg {
			err := a.reactions.Remove(channelID, ts, emojiName)
			return ReactionSentMsg{Err: err}
		}
	}
	return func() tea.Msg {
		err := a.reactions.Add(channelID, ts, emojiName)
		return ReactionSentMsg{Err: err}
	}
}

func (a *App) toggleReactionOnSelectedThread(emojiName string) tea.Cmd {
	reply := a.threadPanel.SelectedReply()
	if reply == nil {
		return nil
	}
	remove := false
	for _, r := range reply.Reactions {
		if r.Emoji == emojiName && r.HasReacted {
			remove = true
			break
		}
	}
	channelID := a.threadPanel.ChannelID()
	a.updateReactionOnMessage(channelID, reply.TS, emojiName, a.currentUserID, remove)
	ts := reply.TS
	if remove {
		return func() tea.Msg {
			err := a.reactions.Remove(channelID, ts, emojiName)
			return ReactionSentMsg{Err: err}
		}
	}
	return func() tea.Msg {
		err := a.reactions.Add(channelID, ts, emojiName)
		return ReactionSentMsg{Err: err}
	}
}

// toggleReactionOnMessageItem toggles the current user's reaction on
// an arbitrary message (not necessarily the selected one). Used by the
// click-on-pill path, which identifies the target message by its
// rendered hit rect rather than by selection. Behavior matches
// toggleReactionOnSelectedMessage: optimistic in-memory update + an
// async tea.Cmd that issues the Slack add/remove call.
func (a *App) toggleReactionOnMessageItem(channelID string, msg messages.MessageItem, emojiName string) tea.Cmd {
	remove := false
	for _, r := range msg.Reactions {
		if r.Emoji == emojiName && r.HasReacted {
			remove = true
			break
		}
	}
	a.updateReactionOnMessage(channelID, msg.TS, emojiName, a.currentUserID, remove)
	ts := msg.TS
	if remove {
		return func() tea.Msg {
			err := a.reactions.Remove(channelID, ts, emojiName)
			return ReactionSentMsg{Err: err}
		}
	}
	return func() tea.Msg {
		err := a.reactions.Add(channelID, ts, emojiName)
		return ReactionSentMsg{Err: err}
	}
}

// copyPermalinkOfSelected resolves the currently-selected message or thread
// reply, calls the permalink fetcher, and returns a tea.Cmd that writes the
// URL to the clipboard and emits a status-bar toast.
func (a *App) copyPermalinkOfSelected() tea.Cmd {
	var channelID, ts string
	switch a.focusedPanel {
	case PanelMessages:
		msg, ok := a.messagepane.SelectedMessage()
		if !ok {
			return nil
		}
		channelID = a.activeChannelID
		ts = msg.TS
	case PanelThread:
		reply := a.threadPanel.SelectedReply()
		if reply == nil {
			return nil
		}
		channelID = a.threadPanel.ChannelID()
		ts = reply.TS
	default:
		return nil
	}
	if channelID == "" || ts == "" {
		return nil
	}
	messageSvc := a.messageSvc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		url, err := messageSvc.Permalink(ctx, channelID, ts)
		if err != nil {
			log.Printf("copy permalink: %v", err)
			return statusbar.PermalinkCopyFailedMsg{}
		}
		if url == "" {
			// No permalink wired (noop service) or Slack returned an
			// empty URL. Silent no-op rather than copying "" with a
			// false success toast.
			return nil
		}
		return tea.BatchMsg{
			tea.SetClipboard(url),
			func() tea.Msg { return statusbar.PermalinkCopiedMsg{} },
		}
	}
}

func (a *App) handleDown() tea.Cmd {
	switch a.focusedPanel {
	case PanelSidebar:
		a.sidebar.MoveDown()
	case PanelMessages:
		if a.view == ViewThreads {
			a.threadsView.MoveDown()
			// j: held-key burst — debounce the network fetch so we
			// don't fire one conversations.replies call per row.
			return a.openSelectedThreadCmd(true)
		}
		a.messagepane.MoveDown()
	case PanelThread:
		a.threadPanel.MoveDown()
	}
	return nil
}

func (a *App) handleUp() tea.Cmd {
	switch a.focusedPanel {
	case PanelSidebar:
		a.sidebar.MoveUp()
	case PanelMessages:
		if a.view == ViewThreads {
			a.threadsView.MoveUp()
			// k: same debounce as j — see handleDown.
			return a.openSelectedThreadCmd(true)
		}
		a.messagepane.MoveUp()
		// If selection reached the top, fetch older messages. The
		// viewport-based path (wheel / PageUp) is handled by
		// scrollFocusedPanel via the same helper.
		if cmd := a.maybeFetchOlderHistory(a.messagepane.AtTop()); cmd != nil {
			return cmd
		}
	case PanelThread:
		a.threadPanel.MoveUp()
	}
	return nil
}

func (a *App) handleGoToBottom() tea.Cmd {
	switch a.focusedPanel {
	case PanelSidebar:
		a.sidebar.GoToBottom()
	case PanelMessages:
		if a.view == ViewThreads {
			a.threadsView.GoToBottom()
			// G is a one-shot jump — fire the fetch immediately.
			return a.openSelectedThreadCmd(false)
		}
		a.messagepane.GoToBottom()
	case PanelThread:
		a.threadPanel.GoToBottom()
	}
	return nil
}

// pageSize returns the number of lines to scroll for a full-page jump in the
// currently-focused panel. Falls back to a sensible default if the layout
// hasn't been measured yet (i.e. before the first render).
func (a *App) pageSize() int {
	h := a.layout.PageHeight(a.focusedPanel)
	if h < 4 {
		h = 4
	}
	// Leave one line of context across the page boundary (vim-style).
	return h - 1
}

// halfPageSize returns the half-page scroll distance for ctrl+u / ctrl+d.
func (a *App) halfPageSize() int {
	n := a.pageSize() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// panelAt is a thin wrapper around panelLayout.PanelAt that forwards
// the App's current height + visibility flags.
func (a *App) panelAt(x, y int) (panel Panel, paneX, paneY int, ok bool) {
	return a.layout.PanelAt(x, y, a.height, a.sidebarVisible, a.threadVisible)
}

// scrollFocusedPanel scrolls the focused panel by delta lines (negative = up)
// WITHOUT advancing selection. This is the keyboard equivalent of the mouse
// wheel: PageUp/PageDown/Ctrl+U/Ctrl+D move the viewport only; the selected
// message/channel stays put (and may scroll off-screen). The next j/k will
// snap the viewport back to keep the (still-)selected item visible because
// hasSnapped == true but snappedSelection != selected once selection moves.
//
// On a scroll-up that lands the messages-pane viewport at the very top, this
// also triggers a fetch of older channel history -- the same UX the
// selection-based AtTop path provides via handleUp.
func (a *App) scrollFocusedPanel(delta int) tea.Cmd {
	if delta == 0 {
		return nil
	}
	n := delta
	if n < 0 {
		n = -n
	}
	switch a.focusedPanel {
	case PanelSidebar:
		if delta < 0 {
			a.sidebar.ScrollUp(n)
		} else {
			a.sidebar.ScrollDown(n)
		}
	case PanelMessages:
		if a.view == ViewThreads {
			if delta < 0 {
				a.threadsView.ScrollUp(n)
			} else {
				a.threadsView.ScrollDown(n)
			}
		} else {
			if delta < 0 {
				a.messagepane.ScrollUp(n)
				return a.maybeFetchOlderHistory(a.messagepane.ViewportAtTop())
			}
			a.messagepane.ScrollDown(n)
		}
	case PanelThread:
		if delta < 0 {
			a.threadPanel.ScrollUp(n)
		} else {
			a.threadPanel.ScrollDown(n)
		}
	}
	return nil
}

// maybeFetchOlderHistory kicks off a backfill of older channel history when
// `atTop` is true, no fetch is already in flight, and a fetcher is wired up.
// Returns nil otherwise. Centralizes the spinner-tick + fetch-cmd batching
// previously duplicated across handleUp / the mouse-wheel handler / page-up.
func (a *App) maybeFetchOlderHistory(atTop bool) tea.Cmd {
	if !atTop || a.fetchingOlder {
		return nil
	}
	a.fetchingOlder = true
	a.messagepane.SetLoading(true)
	chID := a.activeChannelID
	oldestTS := a.messagepane.OldestTS()
	channels := a.channels
	// Kick the spinner tick: if a.loading is already false (workspace
	// fully loaded), no tick is alive and the glyph would freeze on its
	// last frame.
	return tea.Batch(
		tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
			return SpinnerTickMsg{}
		}),
		func() tea.Msg {
			return channels.FetchOlder(chID, oldestTS)
		},
	)
}

// openQuitConfirm raises the centered "Quit slk?" overlay. Called from
// both lowercase `q` and Ctrl+C (the latter intercepted globally so an
// accidental Ctrl+C in any mode never silently kills the app).
func (a *App) openQuitConfirm() {
	a.confirmPrompt.Open(
		"Quit slk?",
		"All workspace connections will close.",
		func() tea.Msg { return tea.Quit() },
	)
	a.SetMode(ModeConfirm)
}

func (a *App) handleEnter() tea.Cmd {
	if a.focusedPanel == PanelSidebar {
		if a.sidebar.IsThreadsSelected() {
			return func() tea.Msg { return ThreadsViewActivatedMsg{} }
		}
		// A section header? Toggle its collapse state and stay in
		// place. Section headers are also navigable via j/k so the
		// user can expand/collapse the firehose Channels section
		// (collapsed by default) without leaving the keyboard.
		if a.sidebar.ToggleCollapseSelected() {
			return nil
		}
		item, ok := a.sidebar.SelectedItem()
		if ok {
			return func() tea.Msg {
				return ChannelSelectedMsg{ID: item.ID, Name: item.Name, Type: item.Type}
			}
		}
	}

	// In the threads-list view, the messages-pane region renders the
	// threadsView model instead of the channel's message history.
	// focusedPanel stays at PanelMessages (we re-use that panel slot),
	// so handleEnter must explicitly route to the highlighted thread
	// here — otherwise the PanelMessages block below falls through to
	// messagepane.SelectedMessage() and opens whatever was highlighted
	// in the underlying channel. Enter also shifts keyboard focus to
	// the thread pane (mirroring the channel-pane Enter semantics:
	// "enter this thread to interact with it"), distinguishing it
	// from the j/k navigation which preserves PanelMessages focus so
	// the user can keep walking the list.
	if a.focusedPanel == PanelMessages && a.view == ViewThreads {
		if _, ok := a.threadsView.SelectedSummary(); !ok {
			return nil
		}
		// Force the open even when openSelectedThreadCmd would
		// otherwise dedup (because j/k or activation already loaded
		// this thread): the user explicitly asked to enter it. We
		// reset the dedup keys so the helper runs its full open
		// path, then re-set focus to the thread pane.
		a.lastOpenedChannelID = ""
		a.lastOpenedThreadTS = ""
		cmd := a.openSelectedThreadCmd(false)
		a.focusedPanel = PanelThread
		return cmd
	}

	if a.focusedPanel == PanelMessages {
		return a.openThreadForSelectedMessage()
	}

	return nil
}

// openThreadForSelectedMessage opens the thread panel for the message
// currently selected in the channel messages pane. Mirrors the Enter
// keypress on PanelMessages; called from both handleEnter and from the
// click-to-open-thread path so the two entry points stay in lockstep.
// Returns nil when there is no selected message or no threadFetcher.
func (a *App) openThreadForSelectedMessage() tea.Cmd {
	msg, ok := a.messagepane.SelectedMessage()
	if !ok {
		return nil
	}
	// Use the message's own TS as the thread parent.
	// If it's already a thread reply, use its ThreadTS instead.
	threadTS := msg.TS
	if msg.ThreadTS != "" && msg.ThreadTS != msg.TS {
		threadTS = msg.ThreadTS
	}
	a.threadVisible = true
	a.statusbar.SetInThread(true)
	a.focusedPanel = PanelThread
	a.threadPanel.SetThread(msg, nil, a.activeChannelID, threadTS)
	a.threadCompose.SetChannel("thread")
	a.applyThreadUnreadBoundary(a.activeChannelID)

	threads := a.threads
	chID := a.activeChannelID
	ts := threadTS
	var batch []tea.Cmd
	if cached := threads.CacheRead(chID, ts); len(cached) > 1 {
		replies := cached[1:] // strip parent; reducer expects replies-only
		batch = append(batch, func() tea.Msg {
			return ThreadRepliesLoadedMsg{ThreadTS: ts, Replies: replies}
		})
	}
	batch = append(batch, func() tea.Msg { return threads.Fetch(chID, ts) })
	return tea.Batch(batch...)
}

func (a *App) SetMode(mode Mode) {
	if mode == ModeInsert {
		a.clearSelections()
	}
	a.mode = mode
	a.statusbar.SetMode(mode)
}

// exitInsertAfterSend mirrors the Esc-from-insert handler so that
// submitting a message returns the user to ModeNormal — matching the
// vim convention where finishing an action drops you back to command
// mode. Used by every send branch in handleInsertMode that actually
// dispatches a message (plain-text and attachment, channel and
// thread). Edits intentionally bypass this and instead exit insert
// mode via cancelEdit on MessageEditedMsg, so a transient edit
// failure doesn't strand the user one keystroke away from resuming.
func (a *App) exitInsertAfterSend() {
	a.SetMode(ModeNormal)
	a.compose.Blur()
	a.threadCompose.Blur()
}

// clearSelections drops any active mouse selection from both message
// and thread panes. Called from any handler that changes focus, mode,
// or visible content in a way that makes the existing selection
// nonsensical (workspace switch, mode change, focus cycle, etc.).
func (a *App) clearSelections() {
	a.messagepane.ClearSelection()
	a.threadPanel.ClearSelection()
}

func (a *App) FocusNext() {
	a.cancelEdit()
	a.clearSelections()
	if !a.sidebarVisible {
		if a.threadVisible {
			if a.focusedPanel == PanelMessages {
				a.focusedPanel = PanelThread
			} else {
				a.focusedPanel = PanelMessages
			}
		}
		return
	}
	switch a.focusedPanel {
	case PanelSidebar:
		a.focusedPanel = PanelMessages
	case PanelMessages:
		if a.threadVisible {
			a.focusedPanel = PanelThread
		} else {
			a.focusedPanel = PanelSidebar
		}
	case PanelThread:
		a.focusedPanel = PanelSidebar
	}
}

func (a *App) FocusPrev() {
	a.cancelEdit()
	a.clearSelections()
	if !a.sidebarVisible {
		if a.threadVisible {
			if a.focusedPanel == PanelThread {
				a.focusedPanel = PanelMessages
			} else {
				a.focusedPanel = PanelThread
			}
		}
		return
	}
	switch a.focusedPanel {
	case PanelSidebar:
		if a.threadVisible {
			a.focusedPanel = PanelThread
		} else {
			a.focusedPanel = PanelMessages
		}
	case PanelMessages:
		a.focusedPanel = PanelSidebar
	case PanelThread:
		a.focusedPanel = PanelMessages
	}
}

func (a *App) ToggleSidebar() {
	a.clearSelections()
	a.sidebarVisible = !a.sidebarVisible
	if !a.sidebarVisible && a.focusedPanel == PanelSidebar {
		a.focusedPanel = PanelMessages
	}
}

func (a *App) ToggleThread() {
	a.clearSelections()
	if a.threadVisible {
		a.CloseThread()
	}
	// Don't open on toggle if no thread is loaded -- use Enter for that
}

func (a *App) CloseThread() {
	a.clearSelections()
	a.threadVisible = false
	a.statusbar.SetInThread(false)
	a.threadPanel.Clear()
	a.threadCompose.Blur()
	// Drop dedup state so a future activation re-opens this thread.
	a.lastOpenedChannelID = ""
	a.lastOpenedThreadTS = ""
	if a.focusedPanel == PanelThread {
		a.focusedPanel = PanelMessages
	}
}

// openSelectedThreadCmd updates UI state for whichever row the threadsview
// has highlighted (so the right thread panel shows the parent immediately),
// then schedules the network fetch.
//
// When debounce is true (j/k key handlers), the fetch is delayed by
// openThreadDebounceDelay and coalesced via pendingThreadFetchGen so a
// held-j burst produces exactly one HTTP call. When debounce is false
// (activation, list reload, G jump), the fetch fires immediately so
// thread content lands without artificial latency.
//
// No-op if the list is empty, no thread fetcher is wired, OR the selected
// thread is already the one open in the right panel (dedup: avoids
// hammering the Slack API and clobbering an in-progress read on every j/k
// press or list reload).
func (a *App) openSelectedThreadCmd(debounce bool) tea.Cmd {
	sum, ok := a.threadsView.SelectedSummary()
	if !ok {
		return nil
	}
	if sum.ChannelID == a.lastOpenedChannelID && sum.ThreadTS == a.lastOpenedThreadTS {
		return nil
	}
	a.lastOpenedChannelID = sum.ChannelID
	a.lastOpenedThreadTS = sum.ThreadTS
	a.threadVisible = true
	a.statusbar.SetInThread(true)
	parent := messages.MessageItem{
		TS:       sum.ParentTS,
		UserID:   sum.ParentUserID,
		UserName: a.userNameFor(sum.ParentUserID),
		Text:     sum.ParentText,
		ThreadTS: sum.ThreadTS,
	}
	a.threadPanel.SetThread(parent, nil, sum.ChannelID, sum.ThreadTS)
	a.threadCompose.SetChannel("thread")
	// Snapshot the parent channel's last_read_ts BEFORE the local mark-
	// read flips below, so the "── new ──" landmark in the thread panel
	// reflects what the user had actually seen prior to opening this
	// thread.
	a.applyThreadUnreadBoundary(sum.ChannelID)
	// Local mark-as-read for the threads list: opening a thread should
	// clear its unread flag in the threads-view list and the sidebar
	// badge. This is presentation-only — it does not call Slack's
	// conversations.mark or advance the parent channel's last_read_ts.
	if a.threadsView.MarkSelectedRead() {
		a.sidebar.SetThreadsUnreadCount(a.threadsView.UnreadCount())
	}
	threads := a.threads
	chID, threadTS := sum.ChannelID, sum.ThreadTS
	if !debounce {
		var batch []tea.Cmd
		if cached := threads.CacheRead(chID, threadTS); len(cached) > 1 {
			replies := cached[1:] // strip parent; reducer expects replies-only
			batch = append(batch, func() tea.Msg {
				return ThreadRepliesLoadedMsg{ThreadTS: threadTS, Replies: replies}
			})
		}
		batch = append(batch, func() tea.Msg { return threads.Fetch(chID, threadTS) })
		return tea.Batch(batch...)
	}
	a.pendingThreadFetchGen++
	gen := a.pendingThreadFetchGen
	return tea.Tick(openThreadDebounceDelay, func(time.Time) tea.Msg {
		return threadFetchDebounceMsg{channelID: chID, threadTS: threadTS, gen: gen}
	})
}

// applyThreadUnreadBoundary tells the thread panel where the unread
// boundary is for `channelID` so it can render a "── new ──" landmark
// before the first reply the user hasn't seen. No-op when no last-read
// fetcher is wired (e.g. in tests).
func (a *App) applyThreadUnreadBoundary(channelID string) {
	if channelID == "" {
		return
	}
	a.threadPanel.SetUnreadBoundary(a.threads.ChannelLastRead(channelID))
}

// scheduleThreadsDirty returns a tea.Cmd that fires a ThreadsListDirtyMsg
// after the configured debounce interval. Used to coalesce bursts of thread
// replies (each delivered as its own NewMessageMsg) into a single re-query
// of the involved-threads list. Returns nil when no workspace is active —
// without an activeTeamID the dirty handler would just drop the message
// anyway.
func (a *App) scheduleThreadsDirty() tea.Cmd {
	if a.activeTeamID == "" {
		return nil
	}
	team := a.activeTeamID
	d := a.threadsDirtyDebounce
	if d == 0 {
		d = 150 * time.Millisecond
	}
	return tea.Tick(d, func(time.Time) tea.Msg {
		return ThreadsListDirtyMsg{TeamID: team}
	})
}

// userNameFor returns the display name for a Slack user ID, falling back
// to the raw ID when the names map has no entry. Returns empty string for
// an empty userID.
func (a *App) userNameFor(userID string) string {
	if userID == "" {
		return ""
	}
	if n, ok := a.userNames[userID]; ok && n != "" {
		return n
	}
	return userID
}

// SetLoadingWorkspaces seeds the startup overlay. Called from
// cmd/slk/main.go at program start. Delegates to workspaceBootstrap;
// kept as an App method for backwards-compatible wiring.
func (a *App) SetLoadingWorkspaces(names []string) {
	a.bootstrap.SetWorkspaces(names)
}

// spinnerGlyph returns the current spinner character for both the
// bootstrap overlay and the messages-pane in-channel spinner. Sourced
// from styles.SpinnerChars indexed by the shared spinnerFrame counter.
func (a *App) spinnerGlyph() string {
	return string(styles.SpinnerChars[a.spinnerFrame])
}

// SetInitialLastReadTS sets the last read timestamp for the initial channel load.
func (a *App) SetInitialLastReadTS(ts string) {
	a.messagepane.SetLastReadTS(ts)
}

// Setters for external use (wiring services)
func (a *App) SetWorkspaces(items []workspace.WorkspaceItem) {
	a.workspaceRail.SetItems(items)
	a.workspaceRail.RefreshUnreads()
	a.workspaceItems = items
	// Update workspace finder items
	var finderItems []workspacefinder.Item
	for _, ws := range items {
		finderItems = append(finderItems, workspacefinder.Item{
			ID:       ws.ID,
			Name:     ws.Name,
			Initials: ws.Initials,
		})
	}
	a.workspaceFinder.SetItems(finderItems)
}

// SetChannels updates the sidebar's channel list, pushes the same set
// into both compose boxes for #-channel autocomplete, and seeds the
// renderers' channel-id -> name map so inbound <#CHANNELID> mentions
// resolve to the user-facing name. Centralizing these updates ensures
// the picker's channel set, the sidebar, and the renderer's resolution
// map never drift from each other (e.g., after a workspace switch, a
// channel join, or a display-name resolution for a DM).
func (a *App) SetChannels(items []sidebar.ChannelItem) {
	a.sidebar.SetItems(items)
	picks := make([]channelpicker.Channel, 0, len(items))
	names := make(map[string]string, len(items))
	for _, ch := range items {
		// Skip entries with empty names (defensive -- they'd never
		// match a typed query and would clutter the empty-query view).
		if ch.Name == "" {
			continue
		}
		picks = append(picks, channelpicker.Channel{
			ID:   ch.ID,
			Name: ch.Name,
			Type: ch.Type,
		})
		names[ch.ID] = ch.Name
	}
	a.compose.SetChannels(picks)
	a.threadCompose.SetChannels(picks)
	a.messagepane.SetChannelNames(names)
	a.threadPanel.SetChannelNames(names)
	a.threadsView.SetChannelNames(names)
}

// SetChannelService wires the App's ChannelService collaborator
// (Slack channels API + local cache + session bookkeeping). Build
// one via NewChannelService from a ChannelServiceFuncs bundle.
func (a *App) SetChannelService(s ChannelService) {
	if s == nil {
		s = noopChannelService
	}
	a.channels = s
}

// SetMessageService wires the App's MessageService collaborator
// (send / edit / delete / mark-unread / permalink). Build one via
// NewMessageService from a MessageServiceFuncs bundle.
func (a *App) SetMessageService(s MessageService) {
	if s == nil {
		s = noopMessageService
	}
	a.messageSvc = s
}

// SetUploader wires the upload callback used by Ctrl+V smart-paste
// when the user submits with attachments.
func (a *App) SetUploader(fn UploadFunc) {
	a.uploader = fn
}

// SetClipboardAvailable signals whether the OS clipboard library
// initialized successfully. When false, the smart-paste code path
// is short-circuited.
func (a *App) SetClipboardAvailable(ok bool) {
	a.clipboardAvailable = ok
}

// SetClipboardReader replaces the clipboard read function. Used by
// tests to inject canned clipboard contents. Pass nil to restore
// the default real clipboard reader.
func (a *App) SetClipboardReader(fn clipboardReader) {
	if fn == nil {
		a.clipboardRead = defaultClipboardReader
		return
	}
	a.clipboardRead = fn
}

// SetThreadService wires the App's ThreadService collaborator
// (fetch / mark / reply / list-fetch + parent-channel last-read).
// Build one via NewThreadService from a ThreadServiceFuncs bundle.
func (a *App) SetThreadService(s ThreadService) {
	if s == nil {
		s = noopThreadService
	}
	a.threads = s
}

// SetReadStateReader installs a callback the sidebar (and any future
// readers) will call at render time to fetch per-channel read state.
// Must be set before the first render for unread dots to appear.
func (a *App) SetReadStateReader(f func() map[string]cache.ReadState) {
	a.sidebar.SetReadStateReader(f)
}

// SetWorkspaceUnreadReader installs the callback the workspace rail
// uses on RefreshUnreads to learn which workspaces have at least one
// channel with has_unread=true.
func (a *App) SetWorkspaceUnreadReader(f func() []string) {
	a.workspaceRail.SetUnreadReader(f)
}

func (a *App) SetChannelFinderItems(items []channelfinder.Item) {
	a.channelFinder.SetItems(items)
}

// SetAvatarFunc sets the function used to get rendered avatars for messages.
func (a *App) SetAvatarFunc(fn messages.AvatarFunc) {
	a.messagepane.SetAvatarFunc(fn)
	a.threadPanel.SetAvatarFunc(fn)
}

// SetImageContext configures the inline-image rendering pipeline on the
// messages pane. Should be called once at startup, before the first
// View(). Pass a zero-valued ImageContext to disable inline rendering.
func (a *App) SetImageContext(ctx imgrender.ImageContext) {
	a.messagepane.SetImageContext(ctx)
	a.threadPanel.SetImageContext(ctx)
}

// SetImageFetcher records the image fetcher so the preview overlay can
// fetch large thumbs on demand. Called once at startup from main.go.
func (a *App) SetImageFetcher(f *imgpkg.Fetcher) {
	a.imageFetcher = f
}

// SetImageProtocol records the active terminal image protocol detected
// at startup so the preview overlay can render itself with the right
// renderer (kitty / sixel / halfblock / off).
func (a *App) SetImageProtocol(p imgpkg.Protocol) {
	a.imgProtocol = p
}

// openImagePreviewCmd looks up the (channel, ts, attIdx) attachment in
// the active messages pane, picks the largest available thumb, and
// returns a tea.Cmd that asynchronously fetches it; on completion the
// cmd dispatches a previewLoadedMsg (or previewErrorMsg) which Update
// turns into an open Preview overlay. Returns nil for any condition
// that makes the open a no-op (no fetcher, attachment missing, no
// thumbs, mismatched channel, etc.).
func (a *App) openImagePreviewCmd(channel, ts string, attIdx int) tea.Cmd {
	return a.previewFetchCmd(channel, ts, attIdx, false)
}

// cycleImagePreviewCmd loads a sibling image (delta = -1 for prev,
// +1 for next; wraps around) into the existing preview overlay. The
// resulting previewLoadedMsg has isCycle = true so the Update arm
// swaps the image rather than constructing a new overlay. No-op when
// the active preview has only one sibling.
func (a *App) cycleImagePreviewCmd(channel, ts string, currentIdx, delta int) tea.Cmd {
	if a.imageFetcher == nil {
		return nil
	}
	msgItem, ok := a.findMessageInActiveChannel(channel, ts)
	if !ok {
		return nil
	}
	imageIdxs := imageAttachmentIndices(msgItem.Attachments)
	if len(imageIdxs) <= 1 {
		return nil
	}
	// Find currentIdx's position within the image-only list and step.
	pos := -1
	for i, idx := range imageIdxs {
		if idx == currentIdx {
			pos = i
			break
		}
	}
	if pos < 0 {
		// currentIdx isn't an image attachment? Treat as start.
		pos = 0
	}
	pos = (pos + delta + len(imageIdxs)) % len(imageIdxs)
	nextAttIdx := imageIdxs[pos]
	return a.previewFetchCmd(channel, ts, nextAttIdx, true)
}

// previewFetchCmd is the shared helper for opening / cycling. cycle
// determines whether the resulting previewLoadedMsg is treated as a
// fresh open or an in-place image swap.
func (a *App) previewFetchCmd(channel, ts string, attIdx int, cycle bool) tea.Cmd {
	if a.imageFetcher == nil {
		return nil
	}
	msgItem, ok := a.findMessageInActiveChannel(channel, ts)
	if !ok {
		return nil
	}
	if attIdx < 0 || attIdx >= len(msgItem.Attachments) {
		return nil
	}
	att := msgItem.Attachments[attIdx]
	if att.FileID == "" || len(att.Thumbs) == 0 {
		return nil
	}

	// Pick the largest available thumb for preview quality.
	var largest messages.ThumbSpec
	for _, t := range att.Thumbs {
		if max(t.W, t.H) > max(largest.W, largest.H) {
			largest = t
		}
	}
	if largest.URL == "" {
		return nil
	}

	// Compute sibling-count / sibling-index over IMAGE attachments only,
	// so the (i/N) caption ignores non-image siblings (e.g. PDFs).
	imageIdxs := imageAttachmentIndices(msgItem.Attachments)
	sibCount := len(imageIdxs)
	sibIndex := 0
	for i, idx := range imageIdxs {
		if idx == attIdx {
			sibIndex = i
			break
		}
	}

	fetcher := a.imageFetcher
	name := att.Name
	fileID := att.FileID
	url := largest.URL
	target := image.Pt(largest.W, largest.H)
	return func() tea.Msg {
		res, err := fetcher.Fetch(context.Background(), imgpkg.FetchRequest{
			Key:    fileID + "-preview",
			URL:    url,
			Target: target,
		})
		if err != nil {
			return previewErrorMsg{Err: err}
		}
		return previewLoadedMsg{
			Name:         name,
			FileID:       fileID,
			Img:          res.Img,
			Path:         res.Source,
			SiblingCount: sibCount,
			SiblingIndex: sibIndex,
			isCycle:      cycle,
		}
	}
}

// previewMetaForOpen returns the (name, sibCount, sibIndex) needed to
// construct a loading-state Preview for the given (channel, ts, attIdx).
// Used to open the overlay synchronously, before the fetch completes,
// so the user sees immediate feedback.
//
// Returns ("", 1, 0) defaults for any miss; callers won't display the
// loading overlay in that case anyway because openImagePreviewCmd
// returns nil.
func (a *App) previewMetaForOpen(channel, ts string, attIdx int) (name string, sibCount, sibIndex int) {
	msgItem, ok := a.findMessageInActiveChannel(channel, ts)
	if !ok {
		return "", 1, 0
	}
	if attIdx < 0 || attIdx >= len(msgItem.Attachments) {
		return "", 1, 0
	}
	imageIdxs := imageAttachmentIndices(msgItem.Attachments)
	sibCount = len(imageIdxs)
	if sibCount == 0 {
		sibCount = 1
	}
	for i, idx := range imageIdxs {
		if idx == attIdx {
			sibIndex = i
			break
		}
	}
	return msgItem.Attachments[attIdx].Name, sibCount, sibIndex
}

// imageAttachmentIndices returns the indices into atts of attachments
// with Kind == "image" and a non-empty FileID. Order preserved.
func imageAttachmentIndices(atts []messages.Attachment) []int {
	out := make([]int, 0, len(atts))
	for i, a := range atts {
		if a.Kind == "image" && a.FileID != "" {
			out = append(out, i)
		}
	}
	return out
}

// findMessageInActiveChannel returns the MessageItem with the matching TS
// in the messages pane (channel) or thread panel, gated on the supplied
// channel ID matching either pane's active channel. Returns ok=false if
// nothing matches. Used by the preview-open path to resolve the
// attachment metadata for a click / `O` keystroke.
func (a *App) findMessageInActiveChannel(channel, ts string) (messages.MessageItem, bool) {
	if channel == a.activeChannelID {
		for _, m := range a.messagepane.Messages() {
			if m.TS == ts {
				return m, true
			}
		}
	}
	if a.threadVisible && channel == a.threadPanel.ChannelID() {
		if parent := a.threadPanel.ParentMsg(); parent.TS == ts {
			return parent, true
		}
		for _, r := range a.threadPanel.Replies() {
			if r.TS == ts {
				return r, true
			}
		}
	}
	return messages.MessageItem{}, false
}

// openInSystemViewerCmd asynchronously launches the OS-native image
// viewer for path. Uses xdg-open on Linux, open on macOS, and
// rundll32 on Windows. Errors are logged and otherwise silent — the
// overlay is already closed by the time this runs.
func openInSystemViewerCmd(path string) tea.Cmd {
	return func() tea.Msg {
		if path == "" {
			return nil
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", path)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
		default:
			cmd = exec.Command("xdg-open", path)
		}
		if err := cmd.Start(); err != nil {
			log.Printf("system viewer launch failed: %v", err)
		}
		return nil
	}
}

// SetUserNames passes the user ID -> display name map to the message pane for mention resolution.
func (a *App) SetUserNames(names map[string]string) {
	a.userNames = names
	a.threadsView.SetUserNames(names)
	a.messagepane.SetUserNames(names)
	a.threadPanel.SetUserNames(names)

	// Build user list for mention picker
	users := make([]mentionpicker.User, 0, len(names))
	for id, displayName := range names {
		users = append(users, mentionpicker.User{
			ID:          id,
			DisplayName: displayName,
			Username:    "",
			IsExternal:  a.externalUsers[id],
		})
	}
	a.compose.SetUsers(users)
	a.threadCompose.SetUsers(users)
}

// SetExternalUsers replaces the set of user IDs known to be Slack
// Connect / shared-channel guests. Sticky: subsequent SetUserNames
// calls consult this map when building mention-picker entries.
// Pass an empty map (or nil) to clear.
func (a *App) SetExternalUsers(externalIDs map[string]bool) {
	if externalIDs == nil {
		externalIDs = map[string]bool{}
	}
	a.externalUsers = externalIDs
	if len(a.userNames) > 0 {
		a.SetUserNames(a.userNames)
	}
}

// SetChannelMembership forwards channel-member IDs to both compose
// pickers. Called by main.go from the membership.Manager.
func (a *App) SetChannelMembership(channelID string, memberIDs []string) {
	a.compose.SetChannelMembership(channelID, memberIDs)
	a.threadCompose.SetChannelMembership(channelID, memberIDs)
}

// SetCustomEmoji rebuilds the emoji entry list (built-ins + the active
// workspace's customs) and pushes it into both compose boxes.
func (a *App) SetCustomEmoji(customs map[string]string) {
	entries := emoji.BuildEntries(customs)
	a.compose.SetEmojiEntries(entries)
	a.threadCompose.SetEmojiEntries(entries)
	if a.reactionPicker != nil {
		a.reactionPicker.SetCustomEmoji(customs)
	}
}

// SetInitialChannel sets the active channel and its messages before the TUI starts.
func (a *App) SetInitialChannel(channelID, channelName string, msgs []messages.MessageItem) {
	a.activeChannelID = channelID
	a.messagepane.SetChannel(channelName, "")
	a.messagepane.SetMessages(msgs)
	a.compose.SetChannel(channelName)
	a.statusbar.SetChannel(channelName)
}

// SetReactionService wires the App's ReactionService collaborator.
// The supplied service handles both reaction add/remove and frecent
// emoji bookkeeping; build one via NewReactionService from
// internal/ui/services.go.
func (a *App) SetReactionService(r ReactionService) {
	if r == nil {
		r = noopReactionService
	}
	a.reactions = r
}

func (a *App) SetCurrentUserID(userID string) {
	a.currentUserID = userID
	a.threadsView.SetSelfUserID(userID)
}

// SetNowTimestampFormatter wires the formatter used to render the
// Timestamp field of an optimistic instant-display message. main.go
// passes a closure that uses cfg.Appearance.TimestampFormat so the
// placeholder's time renders identically to the surrounding messages.
func (a *App) SetNowTimestampFormatter(fn func() string) {
	a.nowTimestampFormatter = fn
}

// nowFormatted returns the current time rendered via the configured
// formatter, falling back to a sensible default when none is wired
// (e.g. in tests).
func (a *App) nowFormatted() string {
	if a.nowTimestampFormatter != nil {
		return a.nowTimestampFormatter()
	}
	return time.Now().Format("3:04 PM")
}



// ActiveChannelID returns the ID of the currently viewed channel.
func (a *App) ActiveChannelID() string {
	return a.activeChannelID
}

// SetWorkspaceSwitcher sets the callback used to switch workspaces.
func (a *App) SetWorkspaceSwitcher(fn SwitchWorkspaceFunc) {
	a.workspaceSwitcher = fn
}

// SetThemeItems sets the available themes for the switcher.
func (a *App) SetThemeItems(names []string) {
	a.themeSwitcher.SetItems(names)
}

// activeTeamName returns the human-readable name of the active workspace,
// falling back to the team ID if no name is known. Used as a label in the
// theme picker header.
func (a *App) activeTeamName() string {
	for _, w := range a.workspaceItems {
		if w.ID == a.activeTeamID {
			if w.Name != "" {
				return w.Name
			}
			return w.ID
		}
	}
	if a.activeTeamID != "" {
		return a.activeTeamID
	}
	return "this workspace"
}

// workspaceNameForActive returns the display name of the active workspace
// (empty string if none). Used as the presence menu header.
func (a *App) workspaceNameForActive() string {
	for _, ws := range a.workspaceItems {
		if ws.ID == a.activeTeamID {
			return ws.Name
		}
	}
	return ""
}

// SetThemeSaver sets the callback for saving the theme selection. The
// callback receives the chosen theme name and the scope (workspace vs.
// global) so the implementation can route to the correct save target.
func (a *App) SetThemeSaver(fn func(name string, scope themeswitcher.ThemeScope)) {
	a.themeSaveFn = fn
}

// SetStatusSetter registers a callback the App invokes when the user picks
// a status action from the presence menu. The callback runs the appropriate
// Slack API call (typically asynchronously) for the active workspace.
func (a *App) SetStatusSetter(fn func(action presencemenu.Action, snoozeMinutes int)) {
	a.setStatusFn = fn
}

// SetThemeOverrides stores the config theme overrides for applying on switch.
func (a *App) SetThemeOverrides(overrides config.Theme) {
	a.themeOverrides = overrides
}

// SetTypingEnabled controls whether typing indicators are shown and sent.
func (a *App) SetTypingEnabled(enabled bool) {
	a.typing.SetEnabled(enabled)
}

// SetMouseWheelLines configures the number of lines the viewport scrolls per
// mouse-wheel notch. Values < 1 are coerced to 1 to guarantee scroll progress.
func (a *App) SetMouseWheelLines(n int) {
	if n < 1 {
		n = 1
	}
	a.mouseWheelLines = n
}

// SetSidebarStaleThreshold configures auto-hiding of inactive
// channels in the sidebar. d is the maximum age (since LastReadTS)
// before a channel is hidden; pass 0 to disable.
func (a *App) SetSidebarStaleThreshold(d time.Duration) {
	a.sidebar.SetStaleThreshold(d)
}

// SetTypingSender sets the callback for sending typing indicators.
func (a *App) SetTypingSender(fn TypingSendFunc) {
	a.typingOut.SetSender(fn)
}

// renderTypingLine returns the styled typing indicator for the current
// channel, or an empty string if no one is typing. Stays on App because
// it pulls in messagepane name resolution and styles. State and
// formatting live in internal/ui/typing.go.
func (a *App) renderTypingLine() string {
	if !a.typing.Enabled() {
		return ""
	}
	userIDs := a.typing.UsersExcluding(a.activeChannelID, a.currentUserID)
	if len(userIDs) == 0 {
		return ""
	}
	// Resolve user IDs to display names
	names := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		name := a.messagepane.ResolveUserName(uid)
		if name == "" {
			name = uid
		}
		names = append(names, name)
	}
	return styles.TypingIndicator.Render(typingIndicatorText(names))
}

func (a *App) View() tea.View {
	// Before the terminal reports its size, we can't lay out the
	// real three-panel UI. Render the loading overlay (or a minimal
	// "Initializing..." fallback) using a sane default canvas so the
	// user sees something immediately instead of a blank altscreen
	// while workspaces connect.
	if a.width == 0 || a.height == 0 {
		var screen string
		if a.bootstrap.IsLoading() {
			// Use a generous default canvas so the centered overlay
			// lands roughly where the user's eye expects it. The
			// real WindowSizeMsg arrives within a frame and the
			// overlay re-renders correctly.
			screen = a.bootstrap.Render(80, 24, a.spinnerGlyph())
		} else {
			screen = "Initializing..."
		}
		v := tea.NewView(screen)
		v.AltScreen = true
		return v
	}

	// Resolve per-pane widths/borders. Compute stores horizontal bands
	// for subsequent mouse hit-testing (panelAt) and surfaces a
	// ThreadAutoHidden flag when the available width can't fit the
	// thread pane at its minimum.
	frame := a.layout.Compute(a.width, a.height, a.workspaceRail.Width(), a.sidebar.Width(), a.sidebarVisible, a.threadVisible)
	if frame.ThreadAutoHidden {
		a.threadVisible = false
		if a.focusedPanel == PanelThread {
			a.focusedPanel = PanelMessages
		}
	}
	contentHeight := frame.ContentHeight
	railWidth := frame.RailWidth
	sidebarWidth := frame.SidebarWidth
	sidebarBorder := frame.SidebarBorder
	msgWidth := frame.MsgWidth
	msgBorder := frame.MsgBorder
	threadWidth := frame.ThreadWidth
	threadBorder := frame.ThreadBorder

	// Helper to force a panel to an exact width and height with a given
	// background color. Uses an explicit width parameter instead of
	// lipgloss.Width(s) to avoid ANSI miscounting in complex rendered content.
	exactSizeBg := func(s string, w, h int, bg color.Color) string {
		return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Background(bg).Render(s)
	}
	exactSize := func(s string, w, h int) string {
		return exactSizeBg(s, w, h, styles.Background)
	}

	themeVer := styles.Version()

	// Render workspace rail (uses rail background so empty cells around
	// the workspace tiles match the rail color, not the message pane).
	railLayoutKey := themeVer
	if c := &a.renderCache.rail; !c.hit(a.workspaceRail.Version(), railWidth, contentHeight, railLayoutKey) {
		out := exactSizeBg(a.workspaceRail.View(contentHeight), railWidth, contentHeight, styles.RailBackground)
		c.store(out, a.workspaceRail.Version(), railWidth, contentHeight, railLayoutKey)
	}
	rail := a.renderCache.rail.output

	var panels []string
	panels = append(panels, rail)

	// Render sidebar. Sidebar uses SidebarBackground so themes with a
	// distinct dark sidebar (e.g. Slack Default) render correctly: both the
	// rounded border's own background and the right-padding fill match the
	// sidebar's panel color rather than the message pane's.
	if a.sidebarVisible {
		sbFocused := a.focusedPanel == PanelSidebar && a.mode != ModeInsert
		// Push focus into the sidebar so the cursor "▌" glyph dims when
		// the panel is unfocused. This must happen BEFORE the panelCache
		// hit-check below, since SetFocused bumps the panel's Version on
		// a flip and the cache key includes that version.
		a.sidebar.SetFocused(sbFocused)
		sbLayoutKey := themeVer<<1 | boolToInt(sbFocused)
		if c := &a.renderCache.sidebar; !c.hit(a.sidebar.Version(), sidebarWidth, contentHeight, sbLayoutKey) {
			borderStyle := lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(styles.Border).
				BorderBackground(styles.SidebarBackground).
				Background(styles.SidebarBackground).
				Width(sidebarWidth)
			if sbFocused {
				borderStyle = lipgloss.NewStyle().
					BorderStyle(lipgloss.ThickBorder()).
					BorderForeground(styles.Primary).
					BorderBackground(styles.SidebarBackground).
					Background(styles.SidebarBackground).
					Width(sidebarWidth)
			}
			sidebarView := a.sidebar.View(contentHeight-2, sidebarWidth)
			sidebarView = borderStyle.Render(sidebarView)
			out := exactSizeBg(sidebarView, sidebarWidth+sidebarBorder, contentHeight, styles.SidebarBackground)
			c.store(out, a.sidebar.Version(), sidebarWidth, contentHeight, sbLayoutKey)
		}
		panels = append(panels, a.renderCache.sidebar.output)
		a.layout.SetSidebarHeight(contentHeight - 2)
	}

	// If the full-screen image preview is open, render a single panel
	// covering the combined messages + thread region instead of the
	// usual two-pane layout. The sidebar, rail, and status bar still
	// render normally so the user can see context. The flag below
	// guards the messages-pane and thread-pane render blocks and is
	// also checked to substitute a single preview panel after them.
	previewActive := a.preview.Active()

	// Render message pane with border.
	//
	// PERF: The naive single-cache approach (key = mix(messagepane.Version,
	// compose.Version)) was the dominant per-keystroke cost at large
	// terminal sizes. Compose dirty()s on every keystroke, which would
	// invalidate the entire bordered+exact-sized panel string and force
	// 5-7 full O(height x width) ansi-aware rescans (JoinVertical x2,
	// ReapplyBgAfterResets, border.Render x3, exactSize x2) over the
	// messages region that hadn't actually changed.
	//
	// Split rendering: cache the bordered messages region (top edge +
	// sides only, no bottom edge) keyed only on messagepane.Version --
	// independent of compose. Render the typing line + compose box with
	// the matching bottom-edge border fresh each frame. Stack them; the
	// border glyphs line up because BorderBottom(false) on top + sides
	// +  BorderTop(false) on bottom + sides yields a continuous panel.
	msgFocused := a.focusedPanel == PanelMessages && a.mode != ModeInsert
	// Push focus into the messages pane so the selected-message "▌"
	// border dims when unfocused. Must happen before the panelCache
	// hit-check (the cache key includes Version, which SetFocused bumps).
	a.messagepane.SetFocused(msgFocused)
	composeFocused := a.mode == ModeInsert && a.focusedPanel != PanelThread
	// Mix the view-mode bit into the layout key so a Channels<->Threads
	// switch invalidates the cached output (the cache is otherwise
	// indistinguishable across views at the same focus/mode/theme).
	msgLayoutKey := themeVer<<3 |
		boolToInt(a.view == ViewThreads)<<2 |
		boolToInt(msgFocused)<<1
	a.compose.SetWidth(msgWidth - 2)
	if previewActive {
		// Skip the messages pane render entirely; we'll emit the
		// preview panel after the thread block.
	} else if a.view == ViewThreads {
		// Threads view: no compose, no typing line. The whole bordered
		// panel is content-stable per threadsView.Version, so we keep
		// the old single-cache path here.
		// Push the current user-name map and self-user id into the
		// threadsview model BEFORE snapshotting its version. SetUserNames
		// and SetSelfUserID are equality-checked (threadsview/model.go), so
		// identical input is a no-op. Reading Version() after these calls
		// means the panel-cache key reflects the post-Set state — fixes a
		// regression where the cache stored output under a stale version
		// and never hit on subsequent renders.
		//
		// Note: channel names are *not* pushed here. They're fanned out
		// from SetChannels (app.go:3295) when the channel list changes,
		// which is rare relative to render frequency, so we keep that
		// allocation off this hot path.
		a.threadsView.SetUserNames(a.userNames)
		a.threadsView.SetSelfUserID(a.currentUserID)
		tvVersion := a.threadsView.Version()
		if c := &a.renderCache.msgPanel; !c.hit(tvVersion, msgWidth, contentHeight, msgLayoutKey) {
			msgBorderStyle := styles.UnfocusedBorder.Width(msgWidth)
			if msgFocused {
				msgBorderStyle = styles.FocusedBorder.Width(msgWidth)
			}
			msgContentHeight := contentHeight - 2
			a.layout.SetMsgHeight(msgContentHeight)
			if msgContentHeight < 3 {
				msgContentHeight = 3
			}
			tvView := a.threadsView.View(msgContentHeight, msgWidth-2)
			tvView = messages.ReapplyBgAfterResets(tvView, messages.BgANSI())
			out := exactSize(
				msgBorderStyle.Render(tvView),
				msgWidth+msgBorder, contentHeight,
			)
			c.store(out, tvVersion, msgWidth, contentHeight, msgLayoutKey)
		}
		panels = append(panels, a.renderCache.msgPanel.output)
	} else {
		// Channel view: split into cached top region + fresh bottom region.
		composeView := a.compose.View(msgWidth-2, composeFocused)
		// Inline pickers stack above the compose box. Both should never be
		// visible simultaneously (mutually exclusive in compose.Update);
		// emoji wins if somehow both are.
		if pickerView := a.compose.EmojiPickerView(msgWidth - 2); pickerView != "" {
			composeView = pickerView + "\n" + composeView
		} else if mentionView := a.compose.MentionPickerView(msgWidth - 2); mentionView != "" {
			composeView = mentionView + "\n" + composeView
		} else if channelView := a.compose.ChannelPickerView(msgWidth - 2); channelView != "" {
			composeView = channelView + "\n" + composeView
		}
		// Add a background-colored spacer line above the compose box
		// (replaces MarginTop which produced unstyled/black margin cells)
		composeSpacer := lipgloss.NewStyle().Background(styles.Background).Width(msgWidth - 2).Render("")
		composeView = composeSpacer + "\n" + composeView
		composeHeight := lipgloss.Height(composeView)
		// Always reserve one row above the compose box for the typing
		// indicator. When nobody is typing we render a blank
		// background-colored spacer in that row so the messages-pane
		// height stays constant -- otherwise a transient typing line
		// would shrink the messages area by one row, producing a
		// spurious "more below" indicator and a visible scroll jump.
		typingLine := a.renderTypingLine()
		if typingLine == "" {
			typingLine = lipgloss.NewStyle().
				Background(styles.Background).
				Width(msgWidth - 2).
				Render("")
		}
		typingHeight := 1
		bottomHeight := composeHeight + typingHeight
		msgContentHeight := contentHeight - 2 - bottomHeight
		a.layout.SetMsgHeight(msgContentHeight)
		if msgContentHeight < 3 {
			msgContentHeight = 3
		}

		// Cached top region: messages + top edge + side edges.
		// NOTE: lipgloss/v2 quirk -- calling .BorderBottom(false) on a
		// style that has BorderStyle() set disables ALL borders unless
		// the other three sides are explicitly enabled with
		// .BorderTop(true).BorderLeft(true).BorderRight(true). Without
		// these, the entire panel renders without any border at all.
		topPanelVersion := a.messagepane.Version()
		topLayoutKey := msgLayoutKey | int64(composeHeight)<<16
		topHeight := msgContentHeight + 1 // +1 for top border edge
		if c := &a.renderCache.msgTop; !c.hit(topPanelVersion, msgWidth, topHeight, topLayoutKey) {
			topBorderStyle := styles.UnfocusedBorder.Width(msgWidth).
				BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
			if msgFocused {
				topBorderStyle = styles.FocusedBorder.Width(msgWidth).
					BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
			}
			msgView := a.messagepane.View(msgContentHeight, msgWidth-2)
			msgView = messages.ReapplyBgAfterResets(msgView, messages.BgANSI())
			out := exactSize(
				topBorderStyle.Render(msgView),
				msgWidth+msgBorder, topHeight,
			)
			c.store(out, topPanelVersion, msgWidth, topHeight, topLayoutKey)
		}
		topBordered := a.renderCache.msgTop.output

		// Fresh bottom region: typing line + compose, with bottom edge.
		// Same lipgloss/v2 quirk applies -- explicit BorderBottom/Left/Right(true)
		// required alongside BorderTop(false), or no border renders.
		bottomBorderStyle := styles.UnfocusedBorder.Width(msgWidth).
			BorderTop(false).BorderLeft(true).BorderRight(true).BorderBottom(true)
		if msgFocused {
			bottomBorderStyle = styles.FocusedBorder.Width(msgWidth).
				BorderTop(false).BorderLeft(true).BorderRight(true).BorderBottom(true)
		}
		bottomInner := lipgloss.JoinVertical(lipgloss.Left, typingLine, composeView)
		bottomInner = messages.ReapplyBgAfterResets(bottomInner, messages.BgANSI())
		bottomBordered := exactSize(
			bottomBorderStyle.Render(bottomInner),
			msgWidth+msgBorder, bottomHeight+1, // +1 for bottom border edge
		)

		panels = append(panels, topBordered+"\n"+bottomBordered)
	}

	// Render thread side panel if visible. Same split-render pattern as
	// the message panel: bordered top region (replies + sides + top edge)
	// is cached on threadPanel.Version; bottom region (compose + sides +
	// bottom edge) is rendered fresh each frame so threadCompose
	// keystrokes don't invalidate the (much larger) replies render.
	if a.threadVisible && threadWidth > 0 && !previewActive {
		threadFocused := a.focusedPanel == PanelThread && a.mode != ModeInsert
		// Push focus into the thread panel so the selected-reply "▌"
		// border dims when unfocused. Must happen before the panelCache
		// hit-check (the cache key includes Version, which SetFocused
		// bumps via dirty()).
		a.threadPanel.SetFocused(threadFocused)
		threadComposeFocused := a.mode == ModeInsert && a.focusedPanel == PanelThread
		threadLayoutKey := themeVer<<2 | boolToInt(threadFocused)<<1 | boolToInt(threadComposeFocused)
		a.threadCompose.SetWidth(threadWidth - 2)

		threadComposeView := a.threadCompose.View(threadWidth-2, threadComposeFocused)
		if pickerView := a.threadCompose.EmojiPickerView(threadWidth - 2); pickerView != "" {
			threadComposeView = pickerView + "\n" + threadComposeView
		} else if mentionView := a.threadCompose.MentionPickerView(threadWidth - 2); mentionView != "" {
			threadComposeView = mentionView + "\n" + threadComposeView
		} else if channelView := a.threadCompose.ChannelPickerView(threadWidth - 2); channelView != "" {
			threadComposeView = channelView + "\n" + threadComposeView
		}
		threadComposeSpacer := lipgloss.NewStyle().Background(styles.Background).Width(threadWidth - 2).Render("")
		threadComposeView = threadComposeSpacer + "\n" + threadComposeView
		threadComposeHeight := lipgloss.Height(threadComposeView)
		threadContentHeight := contentHeight - 2 - threadComposeHeight
		a.layout.SetThreadHeight(threadContentHeight)
		if threadContentHeight < 3 {
			threadContentHeight = 3
		}

		// Cached top region.
		threadTopVersion := a.threadPanel.Version()
		threadTopLayoutKey := threadLayoutKey | int64(threadComposeHeight)<<16
		threadTopHeight := threadContentHeight + 1 // +1 top border edge
		if c := &a.renderCache.thread; !c.hit(threadTopVersion, threadWidth, threadTopHeight, threadTopLayoutKey) {
			// See lipgloss/v2 quirk note on the message-pane top region.
			topBorderStyle := styles.UnfocusedBorder.Width(threadWidth).
				BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
			if threadFocused {
				topBorderStyle = styles.FocusedBorder.Width(threadWidth).
					BorderTop(true).BorderLeft(true).BorderRight(true).BorderBottom(false)
			}
			threadView := a.threadPanel.View(threadContentHeight, threadWidth-2)
			threadView = messages.ReapplyBgAfterResets(threadView, messages.BgANSI())
			out := exactSize(
				topBorderStyle.Render(threadView),
				threadWidth+threadBorder, threadTopHeight,
			)
			c.store(out, threadTopVersion, threadWidth, threadTopHeight, threadTopLayoutKey)
		}
		threadTopBordered := a.renderCache.thread.output

		// Fresh bottom region.
		bottomBorderStyle := styles.UnfocusedBorder.Width(threadWidth).
			BorderTop(false).BorderLeft(true).BorderRight(true).BorderBottom(true)
		if threadFocused {
			bottomBorderStyle = styles.FocusedBorder.Width(threadWidth).
				BorderTop(false).BorderLeft(true).BorderRight(true).BorderBottom(true)
		}
		threadBottomInner := messages.ReapplyBgAfterResets(threadComposeView, messages.BgANSI())
		threadBottomBordered := exactSize(
			bottomBorderStyle.Render(threadBottomInner),
			threadWidth+threadBorder, threadComposeHeight+1, // +1 bottom border edge
		)

		panels = append(panels, threadTopBordered+"\n"+threadBottomBordered)
	}

	// Substitute the preview panel for the messages+thread region.
	// Both branches above were skipped when previewActive was true, so
	// the panels slice currently has rail (+sidebar) and we now append
	// a single overlay panel that spans the combined width.
	if previewActive {
		overlayW := msgWidth + msgBorder
		if a.threadVisible && threadWidth > 0 {
			overlayW += threadWidth + threadBorder
		}
		overlayContent := a.preview.Overlay().View(overlayW, contentHeight, a.imgProtocol)
		overlayPanel := exactSize(overlayContent, overlayW, contentHeight)
		panels = append(panels, overlayPanel)
	}

	content := lipgloss.JoinHorizontal(lipgloss.Top, panels...)
	statusWidth := a.width - railWidth

	// Cache the status row (rail-spacer + statusbar). It depends only on
	// statusbar.Version, statusWidth, and theme.
	if c := &a.renderCache.status; !c.hit(a.statusbar.Version(), statusWidth, 1, themeVer) {
		railSpacer := lipgloss.NewStyle().
			Width(railWidth).
			Background(styles.RailBackground).
			Render("")
		out := lipgloss.JoinHorizontal(lipgloss.Center, railSpacer, a.statusbar.View(statusWidth))
		c.store(out, a.statusbar.Version(), statusWidth, 1, themeVer)
	}
	status := a.renderCache.status.output

	screen := lipgloss.JoinVertical(lipgloss.Left, content, status)

	// Render channel finder overlay on top of existing layout
	if a.channelFinder.IsVisible() {
		screen = a.channelFinder.ViewOverlay(a.width, a.height, screen)
	}

	if a.reactionPicker.IsVisible() {
		screen = a.reactionPicker.ViewOverlay(a.width, a.height, screen)
	}

	if a.confirmPrompt.IsVisible() {
		screen = a.confirmPrompt.ViewOverlay(a.width, a.height, screen)
	}

	if a.workspaceFinder.IsVisible() {
		screen = a.workspaceFinder.ViewOverlay(a.width, a.height, screen)
	}

	if a.themeSwitcher.IsVisible() {
		screen = a.themeSwitcher.ViewOverlay(a.width, a.height, screen)
	}

	if a.presenceMenu.IsVisible() {
		screen = a.presenceMenu.ViewOverlay(a.width, a.height, screen)
	}

	if a.help.IsVisible() {
		screen = a.help.ViewOverlay(a.width, a.height, screen)
	}

	if a.mode == ModePresenceCustomSnooze {
		screen = presencemenu.CustomSnoozeView(a.width, a.height, screen, a.presence.SnoozeBuf())
	}

	if a.bootstrap.IsLoading() {
		screen = a.bootstrap.Render(a.width, a.height, a.spinnerGlyph())
	}

	// All panels are wrapped in exactSize / exactSizeBg before joining, so
	// `screen` is already exactly (a.width, a.height) with every cell themed.
	// We skip the previously-mandatory full-screen lipgloss wrapper -- it
	// walked every cell of the entire ANSI output (~3.4 ms / frame, the
	// single largest cost in the prior profile) just to apply background
	// padding that's already there. If an overlay is active we still need
	// the wrapper because overlay compositors don't always produce exact-
	// sized output; conservatively re-wrap in that case.
	finalScreen := screen
	overlayActive := a.channelFinder.IsVisible() ||
		a.reactionPicker.IsVisible() ||
		a.confirmPrompt.IsVisible() ||
		a.workspaceFinder.IsVisible() ||
		a.themeSwitcher.IsVisible() ||
		a.presenceMenu.IsVisible() ||
		a.mode == ModePresenceCustomSnooze ||
		a.bootstrap.IsLoading()
	if overlayActive {
		finalScreen = lipgloss.NewStyle().
			Width(a.width).
			Height(a.height).
			MaxHeight(a.height).
			Background(styles.Background).
			Render(screen)
	}
	v := tea.NewView(finalScreen)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// cancelEdit exits edit mode, restoring the stashed draft to its
// source compose. Safe to call when no edit is active (no-op).
func (a *App) cancelEdit() {
	if !a.editing.IsActive() {
		return
	}
	stashed := a.editing.StashedDraft()
	switch a.editing.Panel() {
	case PanelMessages:
		a.compose.SetValue(stashed)
		a.compose.SetPlaceholderOverride("")
	case PanelThread:
		a.threadCompose.SetValue(stashed)
		a.threadCompose.SetPlaceholderOverride("")
	}
	a.editing.Clear()
	a.SetMode(ModeNormal)
	a.compose.Blur()
	a.threadCompose.Blur()
}

// isOwnMessage returns whether the given message is owned by the
// current user. Bot/system messages and unauthenticated states fail.
func (a *App) isOwnMessage(m messages.MessageItem) bool {
	return a.currentUserID != "" && m.UserID == a.currentUserID
}

// selectedMessageContext returns the channel ID, message TS, text, owner
// user ID, and panel of the currently-selected message in the focused
// pane. Returns ok=false if nothing is selected or the focused panel is
// not a message-bearing pane.
func (a *App) selectedMessageContext() (channelID, ts, text, userID string, panel Panel, ok bool) {
	switch a.focusedPanel {
	case PanelMessages:
		msg, sel := a.messagepane.SelectedMessage()
		if !sel {
			return "", "", "", "", 0, false
		}
		return a.activeChannelID, msg.TS, msg.Text, msg.UserID, PanelMessages, true
	case PanelThread:
		reply := a.threadPanel.SelectedReply()
		if reply == nil {
			return "", "", "", "", 0, false
		}
		return a.threadPanel.ChannelID(), reply.TS, reply.Text, reply.UserID, PanelThread, true
	default:
		return "", "", "", "", 0, false
	}
}

const maxAttachmentSize = 10 * 1024 * 1024 // 10 MB cap

// submitWithAttachments dispatches the pending attachments + caption
// on the given compose to the configured uploader. It refuses if an
// edit is in progress (chat.update doesn't support file attachments)
// or if there's no active channel / no uploader configured. On
// dispatch, the compose's uploading flag is set so the UI can show
// progress; the actual UploadResultMsg arm in Update clears it.
func (a *App) submitWithAttachments(c *compose.Model) tea.Cmd {
	if a.editing.IsActive() {
		return a.uploadToastCmd("Cannot attach files to an edit (send a new message)", 3*time.Second)
	}
	attachments := c.Attachments()
	if len(attachments) == 0 {
		return nil
	}
	caption := strings.TrimSpace(c.Value())

	var channelID, threadTS string
	if c == &a.threadCompose {
		channelID = a.threadPanel.ChannelID()
		threadTS = a.threadPanel.ThreadTS()
	} else {
		channelID = a.activeChannelID
		threadTS = ""
	}
	if channelID == "" || a.uploader == nil {
		return a.uploadToastCmd("Cannot upload: no active channel", 2*time.Second)
	}

	c.SetUploading(true)
	cmds := []tea.Cmd{
		a.uploader(channelID, threadTS, caption, attachments),
		a.uploadToastCmd(fmt.Sprintf("Uploading 0/%d…", len(attachments)), 30*time.Second),
	}
	return tea.Batch(cmds...)
}

// smartPaste inspects the OS clipboard and dispatches:
//  1. PNG image bytes → attach as image with auto-generated filename.
//  2. Single-line file-path text → attach by path.
//  3. Anything else → insert text into the active compose.
//
// Returns a tea.Cmd that emits the appropriate status-bar toast.
// No-op if clipboard.Init() failed at startup.
func (a *App) smartPaste() tea.Cmd {
	if !a.clipboardAvailable {
		return nil
	}

	// Resolve the active compose pointer.
	target := &a.compose
	if a.focusedPanel == PanelThread && a.threadVisible {
		target = &a.threadCompose
	}

	textBytes := a.clipboardRead(clipboard.FmtText)
	if consumed, cmd := a.tryAttachFromClipboard(target, string(textBytes)); consumed {
		return cmd
	}

	// Text fallback — paste verbatim into the active compose.
	if len(textBytes) > 0 {
		target.SetValue(target.Value() + string(textBytes))
	}
	return nil
}

// tryAttachFromClipboard inspects the OS clipboard for an image and the
// supplied text for a file-path reference, attaching the first match
// to the given compose. Returns consumed=true if an attachment (or an
// explicit refusal toast) was produced; false if neither image nor
// path applied — in which case the caller should fall through to its
// own text-paste behavior.
//
// pathCandidate is the text source to test against resolveFilePath.
// For keystroke smart-paste this is the OS clipboard's text; for
// bracketed-paste this is the PasteMsg's payload.
func (a *App) tryAttachFromClipboard(target *compose.Model, pathCandidate string) (bool, tea.Cmd) {
	// 1. Image bytes from the OS clipboard.
	if imgBytes := a.clipboardRead(clipboard.FmtImage); len(imgBytes) > 0 {
		if int64(len(imgBytes)) > maxAttachmentSize {
			return true, a.uploadToastCmd(
				fmt.Sprintf("Image too large (%s > 10 MB limit)", humanSize(int64(len(imgBytes)))),
				3*time.Second,
			)
		}
		filename := "slk-paste-" + time.Now().Format("2006-01-02-15-04-05") + ".png"
		target.AddAttachment(compose.PendingAttachment{
			Filename: filename,
			Bytes:    imgBytes,
			Mime:     "image/png",
			Size:     int64(len(imgBytes)),
		})
		return true, a.uploadToastCmd(
			fmt.Sprintf("Attached: %s (%s)", filename, humanSize(int64(len(imgBytes)))),
			2*time.Second,
		)
	}

	// 2. File-path text.
	if path, ok := resolveFilePath(pathCandidate); ok {
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			if info.Size() > maxAttachmentSize {
				return true, a.uploadToastCmd("File too large (>10 MB limit)", 3*time.Second)
			}
			if info.Size() == 0 {
				return true, a.uploadToastCmd("Empty file", 2*time.Second)
			}
			filename := filepath.Base(path)
			target.AddAttachment(compose.PendingAttachment{
				Filename: filename,
				Path:     path,
				Mime:     mime.TypeByExtension(filepath.Ext(path)),
				Size:     info.Size(),
			})
			return true, a.uploadToastCmd(
				fmt.Sprintf("Attached: %s (%s)", filename, humanSize(info.Size())),
				2*time.Second,
			)
		}
	}

	return false, nil
}

// beginEditOfSelected starts editing the currently-selected message
// in the focused pane. Returns a no-op + status toast if not owned;
// returns nil if no message is selected.
func (a *App) beginEditOfSelected() tea.Cmd {
	channelID, ts, text, userID, panel, ok := a.selectedMessageContext()
	if !ok {
		return nil
	}
	// Build a synthetic MessageItem just for the ownership check;
	// avoids fetching the full struct twice.
	if !a.isOwnMessage(messages.MessageItem{UserID: userID}) {
		return func() tea.Msg { return statusbar.EditNotOwnMsg{} }
	}
	if channelID == "" || ts == "" {
		return nil
	}

	var stashed string
	switch panel {
	case PanelMessages:
		stashed = a.compose.Value()
		a.compose.SetValue(text)
		a.compose.SetPlaceholderOverride("Editing message — Enter to save, Esc to cancel")
	case PanelThread:
		stashed = a.threadCompose.Value()
		a.threadCompose.SetValue(text)
		a.threadCompose.SetPlaceholderOverride("Editing message — Enter to save, Esc to cancel")
	}

	a.editing.Begin(channelID, ts, panel, stashed)
	a.SetMode(ModeInsert)
	a.focusedPanel = panel
	if panel == PanelThread {
		return a.threadCompose.Focus()
	}
	return a.compose.Focus()
}

// notifyReadStateChanged invalidates the sidebar render cache and
// refreshes the workspace rail's HasUnread flags. Call this whenever
// per-channel read state is mutated via the DB API (i.e., wherever
// a.sidebar.Invalidate() used to suffice). Pairing them prevents the
// rail from going stale when the sidebar updates.
func (a *App) notifyReadStateChanged() {
	a.sidebar.Invalidate()
	a.workspaceRail.RefreshUnreads()
}

// applyChannelMark updates local state for a channel-level read-state
// change (used by both the local mark-unread press and the inbound
// channel_marked WS event). channelID is the channel; ts is the new
// last_read watermark; unreadCount is the canonical unread count to
// show in the sidebar badge.
//
// Idempotent: calling twice with the same values is a no-op past the
// first one (the underlying setters short-circuit on equality).
func (a *App) applyChannelMark(channelID, ts string, unreadCount int) {
	debuglog.Cache("applyChannelMark: channel=%s ts=%s unread_count=%d active=%s",
		channelID, ts, unreadCount, a.activeChannelID)
	if channelID == a.activeChannelID {
		a.messagepane.SetLastReadTS(ts)
	}
	a.notifyReadStateChanged()
}

// applyThreadMark updates local state for a thread-level read-state
// change. read=false means the thread is now unread (move boundary +
// flip threads-view row); read=true means the thread is now read
// (clear boundary + clear threads-view row).
func (a *App) applyThreadMark(channelID, threadTS, ts string, read bool) {
	debuglog.Cache("applyThreadMark: channel=%s thread_ts=%s ts=%s read=%v active=%s",
		channelID, threadTS, ts, read, a.activeChannelID)
	if a.threadVisible &&
		a.threadPanel.ChannelID() == channelID &&
		a.threadPanel.ThreadTS() == threadTS {
		if read {
			a.threadPanel.SetUnreadBoundary("")
		} else {
			a.threadPanel.SetUnreadBoundary(ts)
		}
	}
	if read {
		if a.threadsView.MarkByThreadTSRead(channelID, threadTS) {
			a.sidebar.SetThreadsUnreadCount(a.threadsView.UnreadCount())
		}
	} else {
		if a.threadsView.MarkByThreadTSUnread(channelID, threadTS) {
			a.sidebar.SetThreadsUnreadCount(a.threadsView.UnreadCount())
		}
	}
}

// beginDeleteOfSelected opens the confirmation prompt for deleting the
// currently-selected message in the focused pane. Returns a no-op +
// status toast if not owned, or nil if no message is selected.
func (a *App) beginDeleteOfSelected() tea.Cmd {
	channelID, ts, text, userID, _, ok := a.selectedMessageContext()
	if !ok {
		return nil
	}
	if !a.isOwnMessage(messages.MessageItem{UserID: userID}) {
		return func() tea.Msg { return statusbar.DeleteNotOwnMsg{} }
	}
	if channelID == "" || ts == "" {
		return nil
	}

	preview := strings.ReplaceAll(text, "\n", " ")
	const maxPreview = 80
	if runes := []rune(preview); len(runes) > maxPreview {
		preview = string(runes[:maxPreview]) + "…"
	}

	a.confirmPrompt.Open(
		"Delete message?",
		preview,
		func() tea.Msg {
			return DeleteMessageMsg{ChannelID: channelID, TS: ts}
		},
	)
	a.SetMode(ModeConfirm)
	return nil
}

// markUnreadOfSelected rolls the read watermark backward to the message
// immediately before the currently-selected message in the focused
// pane. Channel pane → emits MarkUnreadMsg with ThreadTS="". Thread
// pane → emits MarkUnreadMsg with ThreadTS=parent ts. Returns nil
// when nothing is selected (silent no-op, matches Edit/Delete).
//
// Boundary semantics:
//   - Channel pane, selection is i-th of N loaded messages →
//     BoundaryTS = messages[i-1].TS (or "0" if i == 0)
//     UnreadCount = N - i
//   - Thread pane, selection is i-th of N replies →
//     BoundaryTS = replies[i-1].TS (or threadTS if i == 0)
//     UnreadCount = 0 (sidebar isn't updated for thread-level)
func (a *App) markUnreadOfSelected() tea.Cmd {
	channelID, ts, _, _, panel, ok := a.selectedMessageContext()
	if !ok || channelID == "" || ts == "" {
		return nil
	}

	switch panel {
	case PanelMessages:
		msgs := a.messagepane.Messages()
		idx := -1
		for i := range msgs {
			if msgs[i].TS == ts {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		boundary := "0"
		if idx > 0 {
			boundary = msgs[idx-1].TS
		}
		unreadCount := len(msgs) - idx
		chID := channelID
		bTS := boundary
		n := unreadCount
		return func() tea.Msg {
			return MarkUnreadMsg{
				ChannelID:   chID,
				ThreadTS:    "",
				BoundaryTS:  bTS,
				UnreadCount: n,
			}
		}

	case PanelThread:
		threadTS := a.threadPanel.ThreadTS()
		replies := a.threadPanel.Replies()
		idx := -1
		for i := range replies {
			if replies[i].TS == ts {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		boundary := threadTS
		if idx > 0 {
			boundary = replies[idx-1].TS
		}
		chID := channelID
		tTS := threadTS
		bTS := boundary
		return func() tea.Msg {
			return MarkUnreadMsg{
				ChannelID:   chID,
				ThreadTS:    tTS,
				BoundaryTS:  bTS,
				UnreadCount: 0,
			}
		}
	}
	return nil
}

// openImagePreviewOfSelected dispatches OpenImagePreviewMsg for the
// first image attachment on the currently-selected message in the
// focused pane (messages or thread). Returns nil if no message is
// selected or the selected message has no image attachment.
func (a *App) openImagePreviewOfSelected() tea.Cmd {
	channelID, ts, _, _, _, ok := a.selectedMessageContext()
	if !ok {
		return nil
	}
	msgItem, found := a.findMessageInActiveChannel(channelID, ts)
	if !found {
		return nil
	}
	for i, att := range msgItem.Attachments {
		if att.Kind == "image" && att.FileID != "" {
			channel := channelID
			messageTS := ts
			idx := i
			return func() tea.Msg {
				return messages.OpenImagePreviewMsg{
					Channel: channel,
					TS:      messageTS,
					AttIdx:  idx,
				}
			}
		}
	}
	return nil
}

// submitEdit emits an EditMessageMsg if the edit text is non-empty.
// Empty text refuses with an inline toast and keeps edit mode open.
func (a *App) submitEdit(rawValue, translated string) tea.Cmd {
	if strings.TrimSpace(rawValue) == "" {
		return func() tea.Msg {
			return editEmptyToastMsg{}
		}
	}
	chID := a.editing.ChannelID()
	ts := a.editing.TS()
	return func() tea.Msg {
		return EditMessageMsg{
			ChannelID: chID,
			TS:        ts,
			NewText:   translated,
		}
	}
}

// truncateReason returns s truncated to max characters with an ellipsis.
// Used for status-bar error toasts.
func truncateReason(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// humanSize formats a byte count as "12 KB", "3.4 MB", or "<1 KB".
func humanSize(size int64) string {
	const kb = 1024
	const mb = 1024 * kb
	switch {
	case size >= mb:
		return fmt.Sprintf("%.1f MB", float64(size)/float64(mb))
	case size >= kb:
		return fmt.Sprintf("%d KB", size/kb)
	default:
		return "<1 KB"
	}
}

// resolveFilePath inspects clipboard text and returns a cleaned,
// absolute file path if it looks like a single-line existing-file
// reference. Returns ok=false on multi-line input, oversized input,
// non-absolute and non-./-relative paths, or paths that don't
// expand. The caller is responsible for the os.Stat / IsRegular
// check and the size check.
func resolveFilePath(text string) (string, bool) {
	s := strings.TrimSpace(text)
	if s == "" || strings.ContainsAny(s, "\n\r") || len(s) > 4096 {
		return "", false
	}
	if strings.HasPrefix(s, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		s = filepath.Join(home, s[2:])
	}
	if !filepath.IsAbs(s) && !strings.HasPrefix(s, "./") {
		return "", false
	}
	return filepath.Clean(s), true
}

// uploadToastCmd builds a tea.Cmd that sets the status bar to the
// given message and schedules a CopiedClearMsg after dur.
func (a *App) uploadToastCmd(text string, dur time.Duration) tea.Cmd {
	return tea.Batch(
		func() tea.Msg {
			a.statusbar.SetToast(text)
			return nil
		},
		tea.Tick(dur, func(time.Time) tea.Msg {
			return statusbar.CopiedClearMsg{}
		}),
	)
}
