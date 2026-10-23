package execution

import (
	"context"

	"sync"
	"testing"

	"time"
)

func TestAppendWaitRegistrationRacesClose(t *testing.T) {
	lifecycle := newAppendWaitLifecycle(context.Background())
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 64 {
		workers.Go(func() {
			<-start
			ctx, finish, ok := lifecycle.begin(context.Background())
			if ok {
				<-ctx.Done()
				finish()
			}
		})
	}
	close(start)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := lifecycle.Close(ctx); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if _, _, ok := lifecycle.begin(context.Background()); ok {
		t.Fatal("accepted a wait after shutdown")
	}
}
