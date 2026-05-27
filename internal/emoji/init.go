package emoji

import (
	"errors"
	"io"
	"os"
	"time"

	"github.com/gammons/slk/internal/debuglog"
	emojilib "github.com/kyokomi/emoji/v2"
	"golang.org/x/term"
)

// InitOptions configures the Init function.
type InitOptions struct {
	// Codemap is the kyokomi-style emoji codemap (":name:" → unicode).
	// Defaults to emojilib.CodeMap() if empty.
	Codemap map[string]string

	// PerProbeTimeout is the timeout for each individual DSR query.
	// Defaults to 200ms.
	PerProbeTimeout time.Duration

	// ProgressFunc, if set, is called periodically during the probe with
	// (current, total) so the caller can render progress.
	ProgressFunc func(current, total int)

	// SkipProbe disables probing entirely. Width() falls back to lipgloss.
	SkipProbe bool

	// ForceProbe runs the probe even if a valid cache exists.
	ForceProbe bool
}

// WillProbe reports whether Init(opts) would attempt to run a fresh probe.
// Returns true if no valid cache exists (or ForceProbe is set) and SkipProbe
// is not set. Useful for callers that want to print a progress message
// before invoking Init.
//
// This function performs the same cache validation as Init but does not
// install any width map or run the probe.
func WillProbe(opts InitOptions) bool {
	if opts.SkipProbe {
		return false
	}
	if ImageModeActive() {
		return false
	}
	if opts.ForceProbe {
		return true
	}

	codemap := opts.Codemap
	if codemap == nil {
		codemap = emojilib.CodeMap()
	}

	terminalKey := IdentifyTerminal()
	cachePath := CachePath(terminalKey)
	c, err := LoadCache(cachePath)
	if err != nil {
		return true // cache missing or unreadable
	}
	if c.Version != CacheVersion {
		return true // stale schema
	}
	if c.CodemapHash != codemapHash(codemap) {
		return true // stale codemap
	}
	return false
}

// Init loads the cache or runs a fresh probe. Must be called once at
// startup, before bubbletea begins. After this returns, Width() is safe
// to call from anywhere.
//
// On any error, Width() falls back to lipgloss.Width(). The error is
// returned for logging but does not prevent the app from running.
func Init(opts InitOptions) error {
	// If we'll need to probe, put the terminal in raw mode for the duration.
	// We don't know whether the probe is needed without consulting the cache,
	// but raw mode is harmless if no probe runs (we'll restore on return).
	if !opts.SkipProbe {
		fd := int(os.Stdin.Fd())
		if term.IsTerminal(fd) {
			st, err := term.MakeRaw(fd)
			if err == nil {
				defer term.Restore(fd, st)
			}
		}
	}

	_, _, err := initWithIO(opts, os.Stdout, os.Stdin)
	return err
}

// initWithIO is the testable core. Returns (loadedFromCache, probed, error).
func initWithIO(opts InitOptions, out io.Writer, in io.Reader) (bool, bool, error) {
	if opts.Codemap == nil {
		opts.Codemap = emojilib.CodeMap()
	}
	if opts.PerProbeTimeout == 0 {
		opts.PerProbeTimeout = 200 * time.Millisecond
	}

	// Image-mode active: width measurement is bypassed for known emoji
	// clusters (see Width() in width.go). The probe data is unused, so
	// skip the probe entirely — saves ~30s of user-visible startup on
	// first launch.
	if ImageModeActive() {
		debuglog.ImgRender("emoji probe skipped: image mode active")
		return false, false, nil
	}

	terminalKey := IdentifyTerminal()
	cachePath := CachePath(terminalKey)
	wantHash := codemapHash(opts.Codemap)

	// Try cache load first (unless ForceProbe).
	if !opts.ForceProbe {
		if c, err := LoadCache(cachePath); err == nil {
			if c.Version == CacheVersion && c.CodemapHash == wantHash {
				setWidthMap(c.Widths)
				// NOTE: previously we also called
				// displaywidth.SetExternalWidths(c.Widths) to inject
				// probed widths into lipgloss's transitive displaywidth
				// table. That API only exists in our fork; we dropped
				// the `replace` directive (issue #7) so `go install`
				// works against upstream displaywidth. Result: slk's
				// own Width() uses probed values, but lipgloss-rendered
				// text falls back to displaywidth's static table for a
				// small set of complex emoji.
				return true, false, nil
			}
		}
	}

	if opts.SkipProbe {
		return false, false, nil
	}

	if out == nil || in == nil {
		return false, false, errors.New("no terminal I/O available; skipping probe")
	}

	// Run probe.
	widths, err := probeAll(out, in, opts.Codemap, opts.PerProbeTimeout)
	if err != nil {
		return false, false, err
	}

	setWidthMap(widths)
	// See note above re: SetExternalWidths and issue #7.

	// Write cache (best effort; failure is silently ignored — the probe
	// will simply re-run on next launch).
	c := &Cache{
		Version:     CacheVersion,
		Terminal:    terminalKey,
		ProbedAt:    time.Now().UTC().Format(time.RFC3339),
		CodemapHash: wantHash,
		Widths:      widths,
	}
	_ = SaveCache(cachePath, c)

	return false, true, nil
}
