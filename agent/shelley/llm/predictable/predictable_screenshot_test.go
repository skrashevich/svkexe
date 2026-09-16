package predictable

import (
	"testing"

	"shelley.exe.dev/claudetool/browse"
)

// The "screenshot image" predictable pattern hardcodes the screenshot
// directory instead of importing claudetool/browse, because this package is
// reachable from the root module (cmd/e3e -> loop) whose go.sum lacks
// browse's chromedp dependencies. That duplication is only safe if something
// notices when the real directory moves, which is what this test is for.
//
// The import is fine here: test-only imports do not become dependencies of
// the package being built, so this does not reintroduce the breakage.
func TestScreenshotImageDirMatchesBrowse(t *testing.T) {
	if screenshotImageDir != browse.ScreenshotDir {
		t.Errorf("screenshotImageDir = %q, but browse.ScreenshotDir = %q; the predictable fixture would write where the UI does not look", screenshotImageDir, browse.ScreenshotDir)
	}
}
