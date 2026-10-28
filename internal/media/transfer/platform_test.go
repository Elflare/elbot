package transfer

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"elbot/internal/delivery"
	"elbot/internal/media/share"
)

func TestPlatformSourcesAndResolverContract(t *testing.T) {
	for _, kind := range []string{"existing", "url", "resolved-id", "resolved-url", "file", "bytes", "empty-bytes"} {
		t.Run(kind, func(t *testing.T) {
			calls, imports := 0, 0
			info := share.MediaInfo{ID: "saved"}
			check := func(ctx context.Context, input share.Input, limits share.ImportLimits) {
				imports++
				if ctx != t.Context() || limits.MaxImportBytes != 100 || limits.DownloadTimeout != time.Second || input.Name != "raw%2520" || input.MIMEType != "text/plain" || input.Source.Platform != "platform" || input.Source.FileID != "file-id" {
					t.Errorf("input=%+v limits=%+v", input, limits)
				}
			}
			objects := &fakeObjects{
				info: func(_ context.Context, id string) (share.MediaInfo, error) {
					if id != "saved" {
						t.Error(id)
					}
					return info, nil
				},
				url: func(ctx context.Context, u string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
					check(ctx, input, limits)
					if u != "https://host/file" {
						t.Error(u)
					}
					return info, nil
				},
				file: func(ctx context.Context, p string, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
					check(ctx, input, limits)
					if p != "/tmp/file" {
						t.Error(p)
					}
					return info, nil
				},
				bytes: func(ctx context.Context, b []byte, input share.Input, limits share.ImportLimits) (share.MediaInfo, error) {
					check(ctx, input, limits)
					if kind == "empty-bytes" && len(b) != 0 {
						t.Error(b)
					}
					return info, nil
				},
			}
			m := NewManager(objects, Options{MaxImportBytes: 100, DownloadTimeout: time.Second})
			segment := PlatformMedia{Name: "raw%2520", PlatformFileID: "file-id"}
			if kind == "existing" {
				segment.MediaID = "saved"
				segment.Size = 1000
			}
			if kind == "url" {
				segment.URL = "https://host/file"
				segment.MIMEType = "text/plain"
			}
			resolver := func(ctx context.Context, s PlatformMedia, limit int64) (delivery.Source, error) {
				calls++
				if ctx != t.Context() || s != segment || limit != 100 {
					t.Error("changed resolver input")
				}
				source := delivery.Source{MIMEType: "text/plain"}
				switch kind {
				case "resolved-id":
					source.MediaID = "saved"
				case "resolved-url":
					source.URL = "https://host/file"
				case "file":
					source.Path = "/tmp/file"
				case "bytes":
					source.Data = []byte("body")
				case "empty-bytes":
					source.Data = []byte{}
				}
				return source, nil
			}
			got, err := m.ImportPlatform(t.Context(), "platform", resolver, segment)
			if err != nil || got != info {
				t.Fatalf("%+v %v", got, err)
			}
			if (kind == "existing" || kind == "url") && calls != 0 {
				t.Fatal("unneeded resolver")
			}
			if kind != "existing" && kind != "url" && calls != 1 {
				t.Fatal("resolver not called")
			}
			if (kind == "existing" || kind == "resolved-id") && imports != 0 {
				t.Fatal("reimported existing ID")
			}
		})
	}
}

func TestPlatformEarlyFailure(t *testing.T) {
	fault := errors.New("resolve failed")
	calls := 0
	resolver := func(context.Context, PlatformMedia, int64) (delivery.Source, error) {
		calls++
		return delivery.Source{}, fault
	}
	m := NewManager(&fakeObjects{}, Options{MaxImportBytes: 10})
	if _, err := m.ImportPlatform(t.Context(), "p", resolver, PlatformMedia{Size: 11}); err == nil || calls != 0 {
		t.Fatal("size limit not applied before resolver")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.ImportPlatform(ctx, "p", resolver, PlatformMedia{}); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatal(err)
	}
	if _, err := m.ImportPlatform(t.Context(), "p", resolver, PlatformMedia{}); !errors.Is(err, fault) || calls != 1 {
		t.Fatal(err)
	}
	if _, err := m.ImportPlatform(t.Context(), "p", nil, PlatformMedia{}); err == nil {
		t.Fatal("accepted absent source")
	}
}

func TestHistoryBatchBudgetDuplicatesAndAssociations(t *testing.T) {
	fault := errors.New("fetch failed")
	loads, fetches, saves := 0, 0, 0
	objects := &fakeObjects{
		load: func(context.Context, share.HistoryKey) (map[int]share.HistoryEntry, error) {
			loads++
			return map[int]share.HistoryEntry{3: {Media: share.MediaInfo{ID: "cached"}, Associated: true, Kind: "file"}, 4: {Media: share.MediaInfo{ID: "output"}}}, nil
		},
		bytes: func(_ context.Context, data []byte, _ share.Input, _ share.ImportLimits) (share.MediaInfo, error) {
			return share.MediaInfo{ID: string(data)}, nil
		},
		associate: func(_ context.Context, _ share.HistoryKey, index int, kind, id string) error {
			saves++
			if kind != "file" {
				t.Error(kind)
			}
			return nil
		},
	}
	m := NewManager(objects, Options{MaxImportBytes: 100})
	row := HistoryMessage{ID: "history", Platform: "p", ScopeID: "s", MessageID: "m", Media: []PlatformMedia{{Type: "file", PlatformFileID: "fail"}, {Type: "file", PlatformFileID: "two"}, {Type: "file"}, {Type: "file"}, {Type: "file"}}}
	var requests []HistoryMediaRequest
	for _, index := range []int{1, 1, 2, 2, 5, 3, 4, 0, 9} {
		requests = append(requests, HistoryMediaRequest{Message: row, Index: index})
	}
	resolver := func(_ context.Context, p string, s PlatformMedia, limit int64) (delivery.Source, error) {
		fetches++
		if p != "p" || limit != 100 {
			t.Error("resolver context")
		}
		if s.PlatformFileID == "fail" {
			return delivery.Source{}, fault
		}
		return delivery.Source{Data: []byte(s.PlatformFileID)}, nil
	}
	results, err := m.GetHistoryMediaBatch(t.Context(), requests, resolver, HistoryFetchOptions{MaxFetchAttempts: 2})
	if err != nil || len(results) != len(requests) || loads != 1 || fetches != 2 || saves != 2 {
		t.Fatalf("results=%+v err=%v loads/fetches/saves=%d/%d/%d", results, err, loads, fetches, saves)
	}
	if !errors.Is(results[0].Err, fault) || results[1].Err != results[0].Err || results[2].Media.ID != "two" || results[3] != results[2] || !errors.Is(results[4].Err, ErrHistoryFetchLimit) || results[5].Media.ID != "cached" || results[6].Media.ID != "output" {
		t.Fatalf("results=%+v", results)
	}
	for _, i := range []int{7, 8} {
		if !errors.Is(results[i].Err, ErrHistoryMediaIndex) {
			t.Fatal(results[i].Err)
		}
	}
}

func TestHistoryBatchExistingIDAndIdentityIsolation(t *testing.T) {
	var keys []share.HistoryKey
	var ids []string
	saves := 0
	objects := &fakeObjects{
		load: func(_ context.Context, key share.HistoryKey) (map[int]share.HistoryEntry, error) {
			keys = append(keys, key)
			return nil, nil
		},
		info: func(_ context.Context, id string) (share.MediaInfo, error) {
			ids = append(ids, id)
			return share.MediaInfo{ID: id}, nil
		},
		associate: func(context.Context, share.HistoryKey, int, string, string) error { saves++; return nil },
	}
	m := NewManager(objects, Options{})
	var requests []HistoryMediaRequest
	for _, p := range []string{"a", "b"} {
		requests = append(requests, HistoryMediaRequest{Message: HistoryMessage{ID: "history", Platform: p, ScopeID: "s", MessageID: "m", Media: []PlatformMedia{{Type: "file", MediaID: p}}}, Index: 1})
	}
	results, err := m.GetHistoryMediaBatch(t.Context(), requests, nil, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Media.ID != "a" || !errors.Is(results[1].Err, ErrHistoryFetchLimit) || len(keys) != 2 || keys[0] == keys[1] || !reflect.DeepEqual(ids, []string{"a"}) || saves != 1 {
		t.Fatalf("results=%+v keys=%+v ids=%v saves=%d err=%v", results, keys, ids, saves, err)
	}
	results, err = m.GetHistoryMediaBatch(t.Context(), requests[1:], nil, HistoryFetchOptions{MaxFetchAttempts: 1})
	if err != nil || results[0].Media.ID != "b" {
		t.Fatalf("budget leaked across calls: %+v %v", results, err)
	}
}

func TestHistoryBatchQueryCancellationAndAssociationFailure(t *testing.T) {
	fault := errors.New("failed")
	for _, stage := range []string{"query", "associate", "cancel", "kind"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			m := NewManager(&fakeObjects{
				load: func(context.Context, share.HistoryKey) (map[int]share.HistoryEntry, error) {
					if stage == "query" {
						return nil, fault
					}
					return map[int]share.HistoryEntry{1: {Media: share.MediaInfo{ID: "one"}, Associated: stage == "kind", Kind: "image"}}, nil
				},
				associate: func(context.Context, share.HistoryKey, int, string, string) error {
					calls++
					if stage == "cancel" {
						cancel()
					}
					if stage == "kind" {
						return nil
					}
					return fault
				},
			}, Options{})
			row := HistoryMessage{Media: []PlatformMedia{{Type: "file"}}}
			results, err := m.GetHistoryMediaBatch(ctx, []HistoryMediaRequest{{Message: row, Index: 1}, {Message: row, Index: 1}}, nil, HistoryFetchOptions{})
			if stage == "query" || stage == "cancel" {
				if !errors.Is(err, fault) || results != nil {
					t.Fatalf("%+v %v", results, err)
				}
				if stage == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			} else {
				if err != nil || calls != 1 || len(results) != 2 {
					t.Fatalf("%+v %v calls=%d", results, err, calls)
				}
				if stage == "associate" && (!errors.Is(results[0].Err, fault) || results[1].Err != results[0].Err) {
					t.Fatal("lost association failure")
				}
			}
		})
	}
}
