// remote_media.go 在现有 HTTP 服务上收发附件，授权和临时持有归当前 WS 连接。
package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"elbot/internal/contextinfo"
	"elbot/internal/delivery"
	"elbot/internal/events"
	"elbot/internal/media"
	platformshare "elbot/internal/platform/share"
	"elbot/internal/workspace"
)

type remoteMediaRef struct {
	attachment remoteAttachment // 发给客户端的连接级描述。
	held       *media.HeldMedia // 连接授权或单次使用独立取得的 Media 持有。
}

var errRemoteAttachmentNotFound = errors.New("CLI attachment not found")

const (
	maxInlineAttachmentBytes = 256 * 1024
	maxInlineAttachmentsSize = 1024 * 1024
)

func (s *RemoteServer) grantFile(ctx context.Context, client *remoteClientConn, mediaID, name, kind string) (remoteAttachment, error) {
	held, err := s.media.Hold(ctx, mediaID)
	if err != nil {
		return remoteAttachment{}, err
	}
	info := held.Info()
	if name == "" {
		name = info.Name
	}
	if kind == "" {
		kind = string(delivery.KindFile)
		if strings.HasPrefix(info.MIMEType, "image/") {
			kind = string(delivery.KindImage)
		}
	}
	file := remoteAttachment{ID: rand.Text(), Name: name, MIMEType: info.MIMEType, Size: info.Size, Kind: kind}
	s.mu.Lock()
	_, online := s.clients[client.id][client]
	if online {
		client.files[file.ID] = &remoteMediaRef{attachment: file, held: held}
	}
	s.mu.Unlock()
	if !online {
		s.media.PublishUsageFinished(ctx, []media.UsageID{held.UsageID})
		return remoteAttachment{}, fmt.Errorf("CLI connection closed")
	}
	return file, nil
}

// borrowFiles 检查连接授权后取得独立 Media 持有，后续使用不依赖连接继续在线。
// Media 调用在锁外执行，避免对象锁或存储延迟阻塞整个远程服务。
func (s *RemoteServer) borrowFiles(ctx context.Context, client *remoteClientConn, ids []string) ([]*remoteMediaRef, error) {
	s.mu.Lock()
	files := make([]*remoteMediaRef, 0, len(ids))
	for _, id := range ids {
		file := client.files[id]
		if file == nil {
			s.mu.Unlock()
			return nil, errRemoteAttachmentNotFound
		}
		files = append(files, file)
	}
	s.mu.Unlock()
	for i, file := range files {
		held, err := s.media.Hold(ctx, file.held.Info().ID)
		if err != nil {
			s.releaseFiles(ctx, files[:i])
			return nil, err
		}
		files[i] = &remoteMediaRef{attachment: file.attachment, held: held}
	}
	return files, nil
}

func (s *RemoteServer) releaseFiles(ctx context.Context, files []*remoteMediaRef) {
	var usages []media.UsageID
	for _, file := range files {
		usages = append(usages, file.held.UsageID)
	}
	if len(usages) != 0 {
		s.media.PublishUsageFinished(ctx, usages)
	}
}

func (s *RemoteServer) handleMedia(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		http.Error(w, "CLI 正在停止", http.StatusServiceUnavailable)
		return
	}
	auth := req.Header.Get("Authorization")
	client := s.mediaClients[strings.TrimPrefix(auth, "Bearer ")]
	if !strings.HasPrefix(auth, "Bearer ") || client == nil {
		s.mu.Unlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.work.Add(1)
	s.mu.Unlock()
	defer s.work.Done()

	ctx, cancel := context.WithCancel(req.Context())
	stopWork := context.AfterFunc(s.workCtx, cancel)
	controller := http.NewResponseController(w)
	interrupted := make(chan struct{})
	stopIO := context.AfterFunc(ctx, func() {
		// 先中断网络读写，再关闭请求体，避免 Close 等待仍阻塞的 Read。
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
		_ = req.Body.Close()
		close(interrupted)
	})
	defer func() {
		stopWork()
		if !stopIO() {
			<-interrupted
			_ = controller.SetReadDeadline(time.Time{})
			_ = controller.SetWriteDeadline(time.Time{})
		}
		cancel()
	}()
	ctx = s.messageContext(ctx, client)
	if req.Method == http.MethodPost {
		s.uploadFile(ctx, client, w, req)
		return
	}
	if req.Method == http.MethodDelete {
		s.mu.Lock()
		file := client.files[req.PathValue("id")]
		delete(client.files, req.PathValue("id"))
		s.mu.Unlock()
		if file == nil {
			http.NotFound(w, req)
			return
		}
		s.releaseFiles(ctx, []*remoteMediaRef{file})
		w.WriteHeader(http.StatusNoContent)
		return
	}
	files, err := s.borrowFiles(ctx, client, []string{req.PathValue("id")})
	if err != nil {
		if errors.Is(err, errRemoteAttachmentNotFound) {
			http.NotFound(w, req)
		} else {
			s.mediaError(ctx, w, err)
		}
		return
	}
	defer s.releaseFiles(ctx, files)
	s.downloadFile(ctx, files[0], w, req)
}

func (s *RemoteServer) uploadFile(ctx context.Context, client *remoteClientConn, w http.ResponseWriter, req *http.Request) {
	reader, err := req.MultipartReader()
	if err != nil {
		http.Error(w, "附件请求格式错误", http.StatusBadRequest)
		return
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "file" || part.FileName() == "" {
		http.Error(w, "附件请求格式错误", http.StatusBadRequest)
		return
	}
	info, err := s.media.ImportReader(ctx, part, -1, media.Input{
		Name: part.FileName(), MIMEType: part.Header.Get("Content-Type"), Source: media.Source{Platform: s.name},
	}, media.ImportLimits{})
	if err != nil {
		s.mediaError(ctx, w, err)
		return
	}
	// 单文件接口不吞掉额外字段或文件，也不调用会排空超大剩余内容的 Part.Close。
	if _, err := reader.NextPart(); err != io.EOF {
		http.Error(w, "附件请求格式错误", http.StatusBadRequest)
		return
	}
	file, err := s.grantFile(ctx, client, info.ID, "", "")
	if err != nil {
		s.mediaError(ctx, w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(file); err != nil {
		// 客户端未收到 ID 时不能主动释放，立即撤销这次授权。
		s.mu.Lock()
		ref := client.files[file.ID]
		delete(client.files, file.ID)
		s.mu.Unlock()
		if ref != nil {
			s.releaseFiles(ctx, []*remoteMediaRef{ref})
		}
		s.logMediaError(ctx, err)
	}
}

// openMediaReader 使归档内容读取随调用取消退出，并在发布使用结束前完成关闭。
func openMediaReader(ctx context.Context, file *remoteMediaRef) (io.ReadCloser, func() error, error) {
	reader, err := file.held.Open(ctx)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	var closeErr error
	closeReader := func() { once.Do(func() { closeErr = reader.Close() }) }
	stop := context.AfterFunc(ctx, closeReader)
	return reader, func() error {
		stop()
		closeReader()
		return closeErr
	}, nil
}

func (s *RemoteServer) downloadFile(ctx context.Context, file *remoteMediaRef, w http.ResponseWriter, req *http.Request) {
	reader, finish, err := openMediaReader(ctx, file)
	if err != nil {
		s.mediaError(ctx, w, err)
		return
	}
	w.Header().Set("Content-Type", file.attachment.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(file.attachment.Size, 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": file.attachment.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if req.Method != http.MethodHead {
		_, err = io.Copy(w, reader)
	}
	if err = errors.Join(err, finish()); err != nil {
		s.logMediaError(ctx, err) // 已写出的响应不能再改写为错误正文。
	}
}

func (s *RemoteServer) prepareInputFiles(ctx context.Context, text string, files []*remoteMediaRef) (string, []platformshare.MessageSegment, error) {
	var segments []platformshare.MessageSegment
	texts := []string{}
	if text != "" {
		texts = append(texts, text)
		segments = append(segments, platformshare.MessageSegment{Type: platformshare.SegmentText, Text: text})
	}
	var total int64
	for _, file := range files {
		info := file.held.Info()
		kind := platformshare.SegmentFile
		if file.attachment.Kind == string(delivery.KindImage) {
			kind = platformshare.SegmentImage
		} else if info.Size <= maxInlineAttachmentBytes && total+info.Size <= maxInlineAttachmentsSize {
			reader, finish, err := openMediaReader(ctx, file)
			if err != nil {
				return "", nil, err
			}
			data, err := io.ReadAll(reader)
			if err = errors.Join(err, finish()); err != nil {
				return "", nil, err
			}
			if bytes.IndexByte(data, 0) < 0 && utf8.Valid(data) {
				total += int64(len(data))
				expanded := workspace.FormatFileExpansion(file.attachment.Name, string(data))
				texts = append(texts, expanded)
				segments = append(segments, platformshare.MessageSegment{Type: platformshare.SegmentText, Text: expanded})
				continue
			}
		}
		segments = append(segments, platformshare.MessageSegment{Type: kind, MediaID: info.ID,
			Name: file.attachment.Name, MIMEType: info.MIMEType, Size: info.Size})
	}
	return strings.Join(texts, "\n\n"), segments, nil
}

func (s *RemoteServer) sendOutputs(ctx context.Context, clients []*remoteClientConn, outputs []delivery.Output) error {
	sent := 0
	var failures []error

clientLoop:
	for _, client := range clients {
		var sendErr error
		for _, out := range outputs {
			msg := outputMessage(remoteMsgChat, out)
			if out.Kind == delivery.KindImage || out.Kind == delivery.KindFile || out.Kind == delivery.KindRecord {
				kind := string(delivery.KindFile)
				if out.Kind == delivery.KindImage {
					kind = string(delivery.KindImage)
				}
				file, err := s.grantFile(ctx, client, out.Source.MediaID, out.Name, kind)
				if err != nil {
					sendErr = err
					break
				}
				msg = remoteMessage{Type: remoteMsgAttachment, Attachment: &file}
			}
			if err := client.write(ctx, msg); err != nil {
				s.removeClient(client)
				_ = client.conn.CloseNow()
				delivery.LogSendResult(ctx, delivery.Target{Platform: s.name, ChatType: contextinfo.ChatPrivate, ChatID: client.id}, delivery.FallbackOutput(outputs), err)
				continue clientLoop
			}
		}
		if sendErr != nil {
			failures = append(failures, fmt.Errorf("client %s: %w", client.id, sendErr))
			delivery.LogSendResult(ctx, delivery.Target{Platform: s.name, ChatType: contextinfo.ChatPrivate, ChatID: client.id}, delivery.FallbackOutput(outputs), sendErr)
		} else {
			sent++
		}
	}
	if sent == 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (s *RemoteServer) logMediaError(ctx context.Context, err error) {
	events.AsyncLog(ctx, events.LogRecord{Category: events.LogCategoryLog, Level: slog.LevelError,
		EventName: "cli_media_failed", Module: "cli", Content: err.Error(),
		Identity: events.LogIdentityFromContext(ctx), ResultStatus: events.LogStatus(err)})
}

func (s *RemoteServer) mediaError(ctx context.Context, w http.ResponseWriter, err error) {
	s.logMediaError(ctx, err)
	http.Error(w, "附件处理失败", http.StatusInternalServerError)
}

func (s *RemoteServer) inputMediaError(ctx context.Context, client *remoteClientConn, id string, err error) {
	s.logMediaError(ctx, err)
	if err := client.write(s.transportCtx, remoteMessage{Type: remoteMsgError, ID: id, Text: "附件处理失败"}); err != nil {
		s.logMediaError(ctx, err)
	}
}
