package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/llm"
	"elbot/internal/media"
	"elbot/internal/platform"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

type dialogueMediaRemote struct {
	media.LocalBackend
	puts, signs atomic.Int64
	signErr     error
}

func (r *dialogueMediaRemote) Put(ctx context.Context, id string, reader io.Reader, size int64, mime string) (string, error) {
	r.puts.Add(1)
	return r.LocalBackend.Put(ctx, id, reader, size, mime)
}

func (r *dialogueMediaRemote) PresignGet(_ context.Context, item *storage.Media, _ time.Duration) (string, error) {
	signs := r.signs.Add(1)
	if r.signErr != nil {
		return "", r.signErr
	}
	return fmt.Sprintf("https://s3.test/%s?signature=%d", item.ID, signs), nil
}

func TestDialogueReusesSignedURLAcrossTurnsAndSessions(t *testing.T) {
	for _, protocol := range []string{"chat", "response"} {
		t.Run(protocol, func(t *testing.T) {
			var center *media.Manager
			remote := &dialogueMediaRemote{LocalBackend: media.LocalBackend{Root: t.TempDir()}}
			configure := func(opts *testAgentOptions) {
				root := t.TempDir()
				center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
				center.Remote = remote
				center.FileDelivery.Backend, center.FileDelivery.MaxDirectBase64Bytes = "hybrid", 1
				opts.Media = center
				if protocol == "response" {
					configureNativeStore(t, opts, "provider", false)
				}
			}
			var a *agentFixture
			var requestCount func() int
			var expectedURL string
			if protocol == "response" {
				f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
					body := inputJSON(request)
					if strings.Contains(body, "data:") || !strings.Contains(body, expectedURL) {
						t.Error("Responses did not reuse the remote URL")
					}
					emitNativeStore(w, fmt.Sprintf("r%d", index), false, nativeText("answer"))
				}, configure)
				a, requestCount = f.agent, func() int { return len(f.captured()) }
			} else {
				client := &fakeLLM{replies: []string{"answer", "answer", "answer"}}
				a = newTestAgent(t, &fakePlatform{}, client, "model", config.ProviderConfig{}, newTestStore(t), configure)
				requestCount = func() int {
					requests := client.chatRequests()
					for _, request := range requests {
						images := 0
						for _, message := range request.Messages {
							for _, segment := range message.Segments {
								if segment.Type == llm.SegmentImage {
									images++
									if segment.URL != expectedURL {
										t.Error("Chat did not reuse the remote URL")
									}
								}
							}
						}
						if images == 0 {
							t.Error("Chat lost the image")
						}
					}
					return len(requests)
				}
			}
			item, err := center.ImportBytes(t.Context(), []byte("shared image"), media.Input{MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			expectedURL = "https://s3.test/" + item.ID + "?signature=1"
			for index := range 3 {
				if index == 2 {
					if err := a.HandleMessage(t.Context(), "/new"); err != nil {
						t.Fatal(err)
					}
				}
				if err := a.HandleMessage(mediaInputContext(t, "image", item.ID), "image"); err != nil {
					t.Fatal(err)
				}
			}
			if calls := requestCount(); calls != 3 || remote.puts.Load() != 1 || remote.signs.Load() != 1 {
				t.Fatalf("requests=%d uploads=%d signatures=%d", calls, remote.puts.Load(), remote.signs.Load())
			}
		})
	}
}

type dialogueSignedURLFaults struct {
	storage.MediaRepository
	readErr, writeErr error
}

func (r *dialogueSignedURLFaults) GetSignedURL(ctx context.Context, id string) (*storage.MediaSignedURL, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	return r.MediaRepository.GetSignedURL(ctx, id)
}

func (r *dialogueSignedURLFaults) UpsertSignedURL(ctx context.Context, record *storage.MediaSignedURL) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	return r.MediaRepository.UpsertSignedURL(ctx, record)
}

type dialogueSignedURLStore struct {
	storage.Store
	repository storage.MediaRepository
}

func (s *dialogueSignedURLStore) Media() storage.MediaRepository { return s.repository }

func TestResponsesMediaFailuresReportAtDialogue(t *testing.T) {
	for _, stage := range []string{"initialize", "read", "sign", "write", "http", "stream"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("test " + stage + " failure")
			want := failure.Error()
			if stage == "initialize" {
				want = "s3 credentials are not configured"
			} else if stage == "http" || stage == "stream" {
				want = "failed to download image: access denied"
			}
			var center *media.Manager
			f := newNativeFixture(t, func(_ int, _ nativeTestRequest, w http.ResponseWriter) {
				if stage == "http" {
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprintf(w, `{"error":{"code":"invalid_image_url","message":%q}}`, want)
				} else {
					fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"code\":\"invalid_image_url\",\"message\":%q}\n\n", want)
				}
			}, func(opts *testAgentOptions) {
				var err error
				center, err = media.NewConfigured(t.Context(), opts.Store, t.TempDir(), config.FileDeliveryConfig{Backend: "hybrid", MaxDirectBase64Bytes: 1}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if stage != "initialize" {
					remote := &dialogueMediaRemote{LocalBackend: media.LocalBackend{Root: t.TempDir()}}
					if stage == "sign" {
						remote.signErr = failure
					}
					center.Remote = remote
				}
				faults := &dialogueSignedURLFaults{MediaRepository: opts.Store.Media()}
				if stage == "read" {
					faults.readErr = failure
				} else if stage == "write" {
					faults.writeErr = failure
				}
				center.Store = &dialogueSignedURLStore{Store: opts.Store, repository: faults}
				opts.Media = center
			})
			item, err := center.ImportBytes(t.Context(), []byte("synthetic image"), media.Input{MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			var terminal, lower, missing atomic.Int32
			logs, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
				switch {
				case record.EventName == "dialogue_failed":
					terminal.Add(1)
				case record.Module == "media":
					lower.Add(1)
				case record.EventName == "dialogue_media_unavailable":
					missing.Add(1)
				}
				return nil
			}, signal.ConnectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Disconnect()
			err = f.agent.HandleMessage(mediaInputContext(t, "image", item.ID), "image")
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("failure lost: %v", err)
			}
			output := awaitNotificationText(t, &f.platform.out, "对话处理失败：")
			if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, want) || terminal.Load() != 1 || lower.Load() != 0 || missing.Load() != 0 {
				t.Fatalf("notification=%q terminal=%d lower=%d missing=%d", output, terminal.Load(), lower.Load(), missing.Load())
			}
			wantCalls := 0
			if stage == "http" || stage == "stream" {
				wantCalls = 1
			}
			if calls := len(f.captured()); calls != wantCalls {
				t.Fatalf("unexpected request/retry count: %d", calls)
			}
		})
	}
}

func mediaInputContext(t *testing.T, text string, ids ...string) context.Context {
	t.Helper()
	segments := []platform.MessageSegment{{Type: platform.SegmentText, Text: text}}
	for _, id := range ids {
		segments = append(segments, platform.MessageSegment{Type: platform.SegmentImage, MediaID: id})
	}
	return platform.WithMessageContext(t.Context(), platform.MessageContext{Segments: segments})
}

func TestDialogueMediaTotalAcrossHistoryAndPending(t *testing.T) {
	for _, protocol := range []string{"chat", "response"} {
		for _, backend := range []string{"base64", "hybrid"} {
			t.Run(protocol+"/"+backend, func(t *testing.T) {
				var center *media.Manager
				remote := &dialogueMediaRemote{LocalBackend: media.LocalBackend{Root: t.TempDir()}}
				configure := func(opts *testAgentOptions) {
					root := filepath.Join(t.TempDir(), "media")
					center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
					center.Remote = remote
					center.FileDelivery.Backend, center.FileDelivery.MaxDirectBase64Bytes = backend, 8388608
					opts.Media = center
					if protocol == "response" {
						configureNativeStore(t, opts, "provider", false)
					}
				}
				var a *agentFixture
				var p *fakePlatform
				var calls func() int
				if protocol == "response" {
					f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
						body := inputJSON(request)
						if index == 1 && (strings.Contains(body, "data:") || strings.Count(body, "https://s3.test/") != 7) {
							t.Errorf("whole replay must use S3, including small and duplicate images")
						}
						emitNativeStore(w, fmt.Sprintf("r%d", index), false, nativeText("answer"))
					}, configure)
					a, p, calls = f.agent, f.platform, func() int { return len(f.captured()) }
				} else {
					p = &fakePlatform{}
					client := &fakeLLM{replies: []string{"answer", "answer"}}
					a = newTestAgent(t, p, client, "model", config.ProviderConfig{}, newTestStore(t), configure)
					calls = func() int { return len(client.chatRequests()) }
				}
				var ids []string
				for i, size := range []int{218805, 2528866, 2296589, 2372322, 2405626, 1} {
					item, err := center.ImportBytes(t.Context(), bytes.Repeat([]byte{byte(i + 1)}, size), media.Input{Name: fmt.Sprintf("synthetic-%d.png", i), MIMEType: "image/png"})
					if err != nil {
						t.Fatal(err)
					}
					ids = append(ids, item.ID)
				}
				if err := a.HandleMessage(mediaInputContext(t, "history", ids[:4]...), "history"); err != nil {
					t.Fatal(err)
				}
				var failed, usage atomic.Int32
				connection, err := events.LogSubmitted.Observe(func(_ context.Context, record events.LogRecord) error {
					if record.EventName == "dialogue_failed" {
						failed.Add(1)
						if record.Identity.SessionID == nil || record.Identity.RequestID == nil || record.Identity.RunID == nil || record.Identity.Attempt == nil || record.ResultStatus != events.ResultFailed {
							t.Error("terminal failure lost identity or status")
						}
					}
					if record.EventName == "llm_usage" {
						usage.Add(1)
					}
					return nil
				}, signal.ConnectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Disconnect()
				err = a.HandleMessage(mediaInputContext(t, "next", ids[4], ids[5], ids[5]), "next")
				if backend == "base64" {
					if err == nil || !strings.Contains(err.Error(), "9822210 bytes") || calls() != 1 || usage.Load() != 0 || failed.Load() != 1 {
						t.Fatalf("err=%v requests=%d usage=%d failures=%d", err, calls(), usage.Load(), failed.Load())
					}
					output := awaitNotificationText(t, &p.out, "对话处理失败：")
					if strings.Count(output, "对话处理失败：") != 1 || !strings.Contains(output, "file_delivery.max_direct_base64_bytes=8388608") {
						t.Fatalf("notification=%q", output)
					}
				} else if err != nil || calls() != 2 || failed.Load() != 0 || remote.puts.Load() != 6 || remote.signs.Load() != 6 {
					t.Fatalf("err=%v requests=%d failures=%d uploads=%d signs=%d", err, calls(), failed.Load(), remote.puts.Load(), remote.signs.Load())
				}
				for _, id := range ids {
					refs, err := center.Store.MediaReferences().ListMediaIDs(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					for _, ref := range refs {
						if ref.OwnerType == "request" {
							t.Fatal("request media hold leaked")
						}
					}
				}
			})
		}
	}
}

func TestResponsesRecoveryRecomputesWholeMediaTotal(t *testing.T) {
	var center *media.Manager
	remote := &dialogueMediaRemote{LocalBackend: media.LocalBackend{Root: t.TempDir()}}
	f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
		body := inputJSON(request)
		if index < 2 && (!strings.Contains(body, "data:") || strings.Contains(body, "https://s3.test/")) {
			t.Error("stored chain must only count the media actually sent")
		}
		if index == 1 {
			nativeChainError(w)
			return
		}
		if index == 2 && (request.PreviousResponseID != "" || strings.Contains(body, "data:") || strings.Count(body, "https://s3.test/") != 2) {
			t.Error("recovery must resolve the full replay once with all media on S3")
		}
		emitNativeStore(w, fmt.Sprintf("r%d", index), true, nativeText("answer"))
	}, func(opts *testAgentOptions) {
		root := t.TempDir()
		center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
		center.Remote = remote
		center.FileDelivery.Backend, center.FileDelivery.MaxDirectBase64Bytes = "hybrid", 4
		opts.Media = center
	})
	for _, data := range []string{"1234", "5"} {
		item, err := center.ImportBytes(t.Context(), []byte(data), media.Input{Name: "image.png", MIMEType: "image/png"})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.agent.HandleMessage(mediaInputContext(t, "next", item.ID), "next"); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.captured()) != 3 || remote.puts.Load() != 2 || remote.signs.Load() != 2 || strings.Contains(f.platform.out.String(), "对话处理失败") {
		t.Fatalf("requests=%d uploads=%d signs=%d", len(f.captured()), remote.puts.Load(), remote.signs.Load())
	}
}

func TestResponsesBranchContinuationUsesWholeMediaTotal(t *testing.T) {
	for _, stored := range []bool{true, false} {
		t.Run(fmt.Sprint(stored), func(t *testing.T) {
			var center *media.Manager
			var imageID string
			executed := 0
			remote := &dialogueMediaRemote{LocalBackend: media.LocalBackend{Root: t.TempDir()}}
			registry := tool.NewRegistry()
			_ = registry.Register(nativeTool{name: "image", run: func(context.Context, tool.CallRequest) (*tool.Result, error) {
				executed++
				return &tool.Result{Segments: []llm.MessageSegment{{Type: llm.SegmentImage, MediaID: imageID}}}, nil
			}})
			f := newNativeFixture(t, func(index int, request nativeTestRequest, w http.ResponseWriter) {
				if index == 0 {
					emitNativeStore(w, "head", stored, nativeCall("image-call", "image", "{}"))
					return
				}
				if index == 2 {
					body := inputJSON(request)
					if strings.Contains(body, "data:") || strings.Count(body, "https://s3.test/") != 2 || (request.PreviousResponseID != "") != stored {
						t.Errorf("branch request used split media policy or wrong chain: %s", body)
					}
				}
				emitNativeStore(w, fmt.Sprintf("r%d", index), stored, nativeText("answer"))
			}, func(opts *testAgentOptions) {
				root := t.TempDir()
				center = media.NewManager(opts.Store, root, &media.LocalBackend{Root: root})
				center.Remote = remote
				center.FileDelivery.Backend, center.FileDelivery.MaxDirectBase64Bytes = "hybrid", 4
				opts.Media, opts.ToolRegistry = center, registry
				configureNativeStore(t, opts, "provider", stored)
			})
			item, err := center.ImportBytes(t.Context(), []byte("1234"), media.Input{Name: "image.png", MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			imageID = item.ID
			if err := f.agent.HandleMessage(t.Context(), "@tool:image run"); err != nil {
				t.Fatal(err)
			}
			rows, err := f.store.Messages().ListBySession(t.Context(), fixtureSession(t, f).ID)
			if err != nil || len(rows) != 4 {
				t.Fatalf("messages=%d err=%v", len(rows), err)
			}
			if _, err := f.agent.execution.sessions.Fork(t.Context(), f.agent.Scope(t.Context()), rows[1].ID); err != nil {
				t.Fatal(err)
			}
			small, err := center.ImportBytes(t.Context(), []byte("5"), media.Input{Name: "tiny.png", MIMEType: "image/png"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.agent.HandleMessage(mediaInputContext(t, "compare", small.ID), "compare"); err != nil {
				t.Fatal(err)
			}
			if len(f.captured()) != 3 || executed != 1 || remote.puts.Load() != 2 || remote.signs.Load() != 2 {
				t.Fatalf("requests=%d tools=%d uploads=%d signs=%d", len(f.captured()), executed, remote.puts.Load(), remote.signs.Load())
			}
		})
	}
}
