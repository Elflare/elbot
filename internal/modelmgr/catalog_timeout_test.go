package modelmgr

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestCatalogFetchDeadline(t *testing.T) {
	for _, delay := range []time.Duration{6 * time.Second, 31 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				opts := testOptions()
				opts.Clients["p"] = &testClient{list: func(ctx context.Context) ([]string, error) {
					select {
					case <-time.After(delay):
						return []string{"slow-model"}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}}
				s := newTestService(t, opts)
				defer s.Close()
				start := time.Now()
				result := s.ModelList("slow-model", ModelListOptions{Fresh: true})
				if delay < 30*time.Second {
					if len(result.Errors) != 0 || len(result.Options) != 1 || result.Options[0].Model != "slow-model" {
						t.Fatalf("slow fetch = %#v", result)
					}
				} else if len(result.Errors) != 1 || !errors.Is(result.Errors[0].Err, context.DeadlineExceeded) || time.Since(start) != 30*time.Second {
					t.Fatalf("deadline result=%#v elapsed=%v", result, time.Since(start))
				}
			})
		})
	}
}
