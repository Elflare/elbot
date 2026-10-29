package skill

import (
	"elbot/internal/media"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
	toolmedia "elbot/internal/tool/media"
	"testing"
)

func newTestMediaRuntime(t *testing.T) *toolmedia.MediaRuntime {
	t.Helper()
	center := mediatest.New(t, media.MediaCenterDeps{Store: testutil.NewSQLiteStore(t)}, t.TempDir())
	return toolmedia.NewMediaRuntime(center, t.TempDir(), mediatest.Publisher(center))
}
