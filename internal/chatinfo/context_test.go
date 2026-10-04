package chatinfo

import (
	"context"
	"sync"
	"testing"
)

func TestPerMessageSnapshots(t *testing.T) {
	if _, ok := FromContext(context.Background()); ok {
		t.Fatal("unexpected info")
	}
	var wg sync.WaitGroup
	for _, platform := range []string{"telegram", "qqonebot"} {
		for _, user := range []string{"first", "second"} {
			wg.Go(func() {
				info := Info{Source: Source{Platform: platform, ScopeID: "group:1"}, Identity: Identity{PlatformUserID: user}}
				ctx := WithInfo(context.Background(), info)
				info.Identity.PlatformUserID = "changed"
				child, cancel := context.WithCancel(ctx)
				cancel()
				got, ok := FromContext(context.WithoutCancel(child))
				if !ok || got.Source.Platform != platform || got.Identity.PlatformUserID != user {
					t.Errorf("snapshot = %+v, %v", got, ok)
				}
			})
		}
	}
	wg.Wait()
}
