package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"elbot/internal/config"
	"elbot/internal/delivery"
	"elbot/internal/media"
	clishare "elbot/internal/platform/cli/share"
	"elbot/internal/platform/share"
	"elbot/internal/testutil"
	"elbot/internal/testutil/mediatest"
	"elbot/internal/workspace"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Track real Media usages so tests check ownership and exactly-once release without mocking storage.
type trackedRemoteMedia struct {
	*media.MediaCenter
	mu       sync.Mutex
	active   map[media.UsageID]string
	ended    map[media.UsageID]int
	finished chan media.UsageID
}

func (m *trackedRemoteMedia) Hold(ctx context.Context, id string) (*media.HeldMedia, error) {
	held, err := m.MediaCenter.Hold(ctx, id)
	if err == nil {
		m.mu.Lock()
		m.active[held.UsageID] = id
		m.mu.Unlock()
	}
	return held, err
}

func (m *trackedRemoteMedia) PublishUsageFinished(ctx context.Context, ids []media.UsageID) {
	m.mu.Lock()
	for _, id := range ids {
		delete(m.active, id)
		m.ended[id]++
		m.finished <- id
	}
	m.mu.Unlock()
	m.MediaCenter.PublishUsageFinished(ctx, ids)
}

func (m *trackedRemoteMedia) assertActive(t *testing.T, want int) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.active) != want {
		t.Fatalf("active usages = %v, want %d", m.active, want)
	}
	for id, count := range m.ended {
		if count != 1 {
			t.Fatalf("usage %s released %d times", id, count)
		}
	}
}

func (m *trackedRemoteMedia) waitFinished(t *testing.T, want media.UsageID) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case id := <-m.finished:
			if id == want {
				return
			}
		case <-timer.C:
			t.Fatalf("usage %s was not released", want)
		}
	}
}

func newRemoteMediaTest(t *testing.T, handler share.MessageHandler, configure ...func(*config.Config)) (*RemoteServer, *trackedRemoteMedia) {
	t.Helper()
	center := mediatest.New(t, media.MediaCenterDeps{Store: testutil.NewSQLiteStore(t)}, t.TempDir(), configure...)
	m := &trackedRemoteMedia{MediaCenter: center, active: map[media.UsageID]string{}, ended: map[media.UsageID]int{}, finished: make(chan media.UsageID, 128)}
	s := testRemoteServer(t)
	s.media = m
	if err := s.Start(t.Context(), handler); err != nil {
		t.Fatal(err)
	}
	return s, m
}

func dialRemoteMedia(t *testing.T, s *RemoteServer) (*websocket.Conn, *remoteClientConn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+s.listener.Addr().String()+defaultRemotePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := wsjson.Write(ctx, conn, remoteMessage{Type: remoteMsgHello, ClientID: "user", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	var hello remoteMessage
	if err := wsjson.Read(ctx, conn, &hello); err != nil || hello.Type != remoteMsgHelloOK || hello.MediaToken == "" {
		t.Fatalf("hello = %+v, error = %v", hello, err)
	}
	s.mu.Lock()
	client := s.mediaClients[hello.MediaToken]
	s.mu.Unlock()
	return conn, client
}

func requestRemoteMedia(t *testing.T, s *RemoteServer, token, method, path, contentType string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+s.listener.Addr().String()+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

func uploadRemoteMedia(t *testing.T, s *RemoteServer, client *remoteClientConn, name string, data []byte) remoteAttachment {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	response, raw := requestRemoteMedia(t, s, client.mediaToken, http.MethodPost, "/cli/v1/files", writer.FormDataContentType(), &body)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, body = %s", response.StatusCode, raw)
	}
	var file remoteAttachment
	if err := json.Unmarshal(raw, &file); err != nil || file.ID == "" || strings.Contains(string(raw), media.IDPrefix) {
		t.Fatalf("upload = %s, error = %v", raw, err)
	}
	return file
}

func TestRemoteMediaHTTPAuthorizationAndRelease(t *testing.T) {
	s, m := newRemoteMediaTest(t, nil)
	firstWS, first := dialRemoteMedia(t, s)
	_, second := dialRemoteMedia(t, s)
	if first.mediaToken == second.mediaToken {
		t.Fatal("connections share a media token")
	}
	for _, token := range []string{"", "wrong", "secret"} {
		response, _ := requestRemoteMedia(t, s, token, http.MethodPost, "/cli/v1/files", "", nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthorized upload = %d", response.StatusCode)
		}
	}
	response, _ := requestRemoteMedia(t, s, first.mediaToken, http.MethodPost, "/cli/v1/files", "text/plain", strings.NewReader("bad"))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid upload = %d", response.StatusCode)
	}
	payload := []byte{0, 1, 2, 3, 255}
	file := uploadRemoteMedia(t, s, first, "文件.bin", payload)
	path := "/cli/v1/files/" + file.ID
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		response, _ = requestRemoteMedia(t, s, second.mediaToken, method, path, "", nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("cross-connection %s = %d", method, response.StatusCode)
		}
	}
	response, data := requestRemoteMedia(t, s, first.mediaToken, http.MethodGet, path, "", nil)
	if response.StatusCode != http.StatusOK || !bytes.Equal(data, payload) || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download = %d, %v, %v", response.StatusCode, data, response.Header)
	}
	response, data = requestRemoteMedia(t, s, first.mediaToken, http.MethodHead, path, "", nil)
	if response.StatusCode != http.StatusOK || len(data) != 0 || response.ContentLength != int64(len(payload)) {
		t.Fatalf("HEAD = %d, %d, %d", response.StatusCode, len(data), response.ContentLength)
	}
	response, _ = requestRemoteMedia(t, s, first.mediaToken, http.MethodDelete, path, "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", response.StatusCode)
	}
	response, _ = requestRemoteMedia(t, s, first.mediaToken, http.MethodGet, path, "", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted download = %d", response.StatusCode)
	}
	m.assertActive(t, 0)
	file = uploadRemoteMedia(t, s, first, "disconnect.bin", payload)
	s.mu.Lock()
	usage := first.files[file.ID].held.UsageID
	s.mu.Unlock()
	firstWS.CloseNow()
	m.waitFinished(t, usage)
	response, _ = requestRemoteMedia(t, s, first.mediaToken, http.MethodGet, "/cli/v1/files/"+file.ID, "", nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disconnected token = %d", response.StatusCode)
	}
	s.removeClient(first)
	m.assertActive(t, 0)
}

func TestRemoteMediaFailedUploadsLeaveNoGrants(t *testing.T) {
	for _, extraPart := range []bool{false, true} {
		t.Run(fmt.Sprint(extraPart), func(t *testing.T) {
			s, m := newRemoteMediaTest(t, nil, func(cfg *config.Config) { cfg.PlatformFiles.MaxReceiveFileBytes = 8 })
			_, client := dialRemoteMedia(t, s)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "file.txt")
			if err != nil {
				t.Fatal(err)
			}
			payload, wantStatus := "too large", http.StatusInternalServerError
			if extraPart {
				payload, wantStatus = "small", http.StatusBadRequest
			}
			if _, err := io.WriteString(part, payload); err != nil {
				t.Fatal(err)
			}
			if extraPart {
				if err := writer.WriteField("extra", "unexpected"); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			response, data := requestRemoteMedia(t, s, client.mediaToken, http.MethodPost, "/cli/v1/files", writer.FormDataContentType(), &body)
			if response.StatusCode != wantStatus || (!extraPart && string(data) != "附件处理失败\n") {
				t.Fatalf("upload = %d, %s", response.StatusCode, data)
			}
			s.mu.Lock()
			grants := len(client.files)
			s.mu.Unlock()
			if grants != 0 {
				t.Fatalf("failed upload left %d grants", grants)
			}
			m.assertActive(t, 0)
		})
	}
}

type failedRemoteMediaWriter struct{ *httptest.ResponseRecorder }

func (failedRemoteMediaWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestRemoteMediaFailedUploadResponseReleasesGrant(t *testing.T) {
	s, m := newRemoteMediaTest(t, nil)
	_, client := dialRemoteMedia(t, s)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "body"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/cli/v1/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	s.uploadFile(t.Context(), client, failedRemoteMediaWriter{httptest.NewRecorder()}, req)
	s.mu.Lock()
	grants := len(client.files)
	s.mu.Unlock()
	if grants != 0 {
		t.Fatalf("failed response left %d grants", grants)
	}
	m.assertActive(t, 0)
}

func TestRemoteMediaInputNormalization(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
		kind share.MessageSegmentType
	}{
		{"code.go", []byte("package main\n// ```\n"), share.SegmentText},
		{"binary.bin", []byte{'a', 0, 'b'}, share.SegmentFile},
		{"invalid.txt", []byte{255, 254}, share.SegmentFile},
		{"large.txt", bytes.Repeat([]byte("x"), maxInlineAttachmentBytes+1), share.SegmentFile},
		{"image.png", imageData.Bytes(), share.SegmentImage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := make(chan share.MessageContext, 1)
			texts := make(chan string, 1)
			s, _ := newRemoteMediaTest(t, func(_ context.Context, text string, msg share.MessageContext) error {
				texts <- text
				inputs <- msg
				return nil
			})
			conn, client := dialRemoteMedia(t, s)
			file := uploadRemoteMedia(t, s, client, tc.name, tc.data)
			if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, FileIDs: []string{file.ID}}); err != nil {
				t.Fatal(err)
			}
			select {
			case input := <-inputs:
				text := <-texts
				if len(input.Segments) != 1 || input.Segments[0].Type != tc.kind || input.RawText != "" {
					t.Fatalf("input = %+v", input)
				}
				if tc.kind == share.SegmentText {
					want := workspace.FormatFileExpansion(tc.name, string(tc.data))
					if text != want || input.Segments[0].Text != want {
						t.Fatalf("expanded text = %q", text)
					}
				} else if text != "" || !media.ValidID(input.Segments[0].MediaID) {
					t.Fatalf("media input text = %q, segments = %+v", text, input.Segments)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("attachment-only input was dropped")
			}
		})
	}
}

func TestRemoteMediaInputTotalLimitAndInvalidIDs(t *testing.T) {
	inputs := make(chan share.MessageContext, 1)
	s, m := newRemoteMediaTest(t, func(_ context.Context, _ string, msg share.MessageContext) error { inputs <- msg; return nil })
	conn, client := dialRemoteMedia(t, s)
	_, other := dialRemoteMedia(t, s)
	var ids []string
	for i := range 5 {
		data := bytes.Repeat([]byte{byte('a' + i)}, maxInlineAttachmentBytes)
		ids = append(ids, uploadRemoteMedia(t, s, client, fmt.Sprintf("%d.txt", i), data).ID)
	}
	for _, invalid := range [][]string{{ids[0], "missing"}, {ids[0], uploadRemoteMedia(t, s, other, "other.txt", []byte("other")).ID}} {
		if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, ID: "bad", Text: "keep", FileIDs: invalid}); err != nil {
			t.Fatal(err)
		}
		msg := readTestRemote(t, conn)
		if msg.Type != remoteMsgError || msg.ID != "bad" || msg.Text != "附件处理失败" {
			t.Fatalf("invalid input response = %+v", msg)
		}
		m.assertActive(t, 6)
		select {
		case <-inputs:
			t.Fatal("partial input was submitted")
		default:
		}
	}
	if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, Text: " hello ", FileIDs: ids}); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-inputs:
		if input.RawText != " hello " || len(input.Segments) != 6 || input.Segments[0].Text != "hello" || input.Segments[5].Type != share.SegmentFile {
			t.Fatalf("input = %+v", input)
		}
		for _, segment := range input.Segments[1:5] {
			if segment.Type != share.SegmentText {
				t.Fatal("text within total limit was not expanded")
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("input missing")
	}
}

func TestRemoteMediaInflightInputSurvivesRevocation(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s, m := newRemoteMediaTest(t, func(context.Context, string, share.MessageContext) error {
				close(entered)
				<-release
				return nil
			})
			conn, client := dialRemoteMedia(t, s)
			file := uploadRemoteMedia(t, s, client, "file.bin", []byte{0, 1, 2})
			s.mu.Lock()
			usage := client.files[file.ID].held.UsageID
			s.mu.Unlock()
			if err := wsjson.Write(t.Context(), conn, remoteMessage{Type: remoteMsgInput, FileIDs: []string{file.ID}}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("input missing")
			}
			m.assertActive(t, 2)
			if disconnect {
				conn.CloseNow()
			} else {
				response, _ := requestRemoteMedia(t, s, client.mediaToken, http.MethodDelete, "/cli/v1/files/"+file.ID, "", nil)
				if response.StatusCode != http.StatusNoContent {
					t.Fatal(response.StatusCode)
				}
			}
			m.waitFinished(t, usage)
			m.assertActive(t, 1)
			once.Do(func() { close(release) })
			s.work.Wait()
			m.assertActive(t, 0)
		})
	}
}

func TestRemoteMediaOutputKeepsOrderAndOutlivesSendFiles(t *testing.T) {
	s, m := newRemoteMediaTest(t, nil)
	one, first := dialRemoteMedia(t, s)
	two, second := dialRemoteMedia(t, s)
	info, err := m.ImportReader(t.Context(), strings.NewReader("output"), 6, media.Input{Name: "output.txt"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	source, usage, err := m.ResolveForOutput(t.Context(), info.ID)
	if err != nil || source.MediaID != info.ID {
		t.Fatalf("resolved source = %+v, error = %v", source, err)
	}
	defer m.MediaCenter.PublishUsageFinished(t.Context(), []media.UsageID{usage})
	_, err = s.SendTo(t.Context(), delivery.Target{ChatID: "user"}, []delivery.Output{
		delivery.Text("before"), {Kind: delivery.KindFile, Name: "named.txt", Source: source}, delivery.Text("after"),
	})
	if err != nil {
		t.Fatal(err)
	}
	m.MediaCenter.PublishUsageFinished(t.Context(), []media.UsageID{usage})
	if err := m.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source.Path); !os.IsNotExist(err) {
		t.Fatalf("temporary delivery file still exists: %v", err)
	}
	var ids []string
	for i, conn := range []*websocket.Conn{one, two} {
		if msg := readTestRemote(t, conn); msg.Type != remoteMsgChat || msg.Text != "before" {
			t.Fatal(msg)
		}
		msg := readTestRemote(t, conn)
		if msg.Type != remoteMsgAttachment || msg.Attachment == nil || msg.Attachment.Name != "named.txt" || msg.Attachment.Kind != "file" {
			t.Fatal(msg)
		}
		ids = append(ids, msg.Attachment.ID)
		if msg := readTestRemote(t, conn); msg.Type != remoteMsgChat || msg.Text != "after" {
			t.Fatal(msg)
		}
		client := []*remoteClientConn{first, second}[i]
		response, data := requestRemoteMedia(t, s, client.mediaToken, http.MethodGet, "/cli/v1/files/"+ids[i], "", nil)
		if response.StatusCode != http.StatusOK || string(data) != "output" {
			t.Fatalf("deferred download = %d, %s", response.StatusCode, data)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("receivers share an attachment grant")
	}
	response, _ := requestRemoteMedia(t, s, first.mediaToken, http.MethodGet, "/cli/v1/files/"+ids[1], "", nil)
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("output grant leaked across connections")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m.assertActive(t, 0)
}

type enteringRemoteMedia struct {
	clishare.Media
	entered chan struct{}
}

func (m enteringRemoteMedia) ImportReader(ctx context.Context, r io.Reader, size int64, spec media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	close(m.entered)
	return m.Media.ImportReader(ctx, r, size, spec, limits)
}

func TestRemoteMediaCloseInterruptsSlowUpload(t *testing.T) {
	s, m := newRemoteMediaTest(t, nil)
	_, client := dialRemoteMedia(t, s)
	entered := make(chan struct{})
	s.mu.Lock()
	s.media = enteringRemoteMedia{Media: m, entered: entered}
	s.mu.Unlock()
	conn, err := net.Dial("tcp", s.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := "--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"slow.bin\"\r\n\r\npartial"
	_, err = fmt.Fprintf(conn, "POST /cli/v1/files HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\nContent-Type: multipart/form-data; boundary=boundary\r\nContent-Length: 1000000\r\n\r\n%s", client.mediaToken, body)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("upload never entered Media")
	}
	s.BeginClose()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("slow upload blocked Wait: %v", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m.assertActive(t, 0)
}

func TestRemoteMediaCloseInterruptsSlowDownload(t *testing.T) {
	s, m := newRemoteMediaTest(t, nil)
	_, client := dialRemoteMedia(t, s)
	payload := bytes.Repeat([]byte{0}, 16*1024*1024)
	info, err := m.ImportReader(t.Context(), bytes.NewReader(payload), int64(len(payload)), media.Input{Name: "large.bin"}, media.ImportLimits{})
	if err != nil {
		t.Fatal(err)
	}
	file, err := s.grantFile(t.Context(), client, info.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTCP("tcp", nil, s.listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "GET /cli/v1/files/%s HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer %s\r\n\r\n", file.ID, client.mediaToken); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	s.BeginClose()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("slow download blocked Wait: %v", err)
	}
	m.assertActive(t, 1)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m.assertActive(t, 0)
}
