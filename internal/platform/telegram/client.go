package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"elbot/internal/delivery"
)

type apiClient struct {
	cfg   Config
	http  *http.Client
	token string
}

type mediaSource struct {
	URL  string
	Path string
	Data []byte
	Name string
}

func newAPIClient(cfg Config) *apiClient {
	return &apiClient{cfg: cfg, http: newHTTPClient(cfg), token: cfg.token()}
}

func newHTTPClient(cfg Config) *http.Client {
	proxyURL, _ := cfg.proxyURL()
	if proxyURL == nil {
		return &http.Client{}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	return &http.Client{Transport: transport}
}

func (c *apiClient) callRaw(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	return callTelegram[json.RawMessage](c, ctx, method, params, c.cfg.apiTimeout())
}

func (c *apiClient) getMe(ctx context.Context) (user, error) {
	return callTelegram[user](c, ctx, "getMe", nil, c.cfg.apiTimeout())
}

func (c *apiClient) getUpdates(ctx context.Context, offset int64) ([]update, error) {
	req := getUpdatesRequest{
		Offset:  offset,
		Timeout: int(c.cfg.pollTimeout() / time.Second),
		AllowedUpdates: []string{
			"message",
			"callback_query",
		},
	}
	return callTelegram[[]update](c, ctx, "getUpdates", req, c.cfg.pollTimeout()+c.cfg.apiTimeout())
}

func (c *apiClient) sendMessage(ctx context.Context, req sendMessageRequest) (sentMessage, error) {
	msg, err := callTelegram[sentMessage](c, ctx, "sendMessage", req, c.cfg.apiTimeout())
	if err == nil || req.ParseMode == "" || !isTelegramParseError(err) {
		return msg, err
	}
	req.ParseMode = ""
	return callTelegram[sentMessage](c, ctx, "sendMessage", req, c.cfg.apiTimeout())
}

func (c *apiClient) editMessageText(ctx context.Context, req editMessageTextRequest) (sentMessage, error) {
	msg, err := callTelegram[sentMessage](c, ctx, "editMessageText", req, c.cfg.apiTimeout())
	if err == nil || req.ParseMode == "" || !isTelegramParseError(err) {
		return msg, err
	}
	req.ParseMode = ""
	return callTelegram[sentMessage](c, ctx, "editMessageText", req, c.cfg.apiTimeout())
}

func (c *apiClient) setMyCommands(ctx context.Context, commands []botCommand) error {
	_, err := callTelegram[bool](c, ctx, "setMyCommands", setMyCommandsRequest{Commands: commands}, c.cfg.apiTimeout())
	return err
}

func (c *apiClient) sendRichMessage(ctx context.Context, req sendRichMessageRequest) (sentMessage, error) {
	return callTelegram[sentMessage](c, ctx, "sendRichMessage", req, c.cfg.apiTimeout())
}

func (c *apiClient) sendRichMessageDraft(ctx context.Context, req sendRichMessageDraftRequest) error {
	_, err := callTelegram[bool](c, ctx, "sendRichMessageDraft", req, c.cfg.apiTimeout())
	return err
}

func (c *apiClient) answerCallbackQuery(ctx context.Context, id, text string) error {
	_, err := callTelegram[bool](c, ctx, "answerCallbackQuery", answerCallbackQueryRequest{CallbackQueryID: id, Text: text}, c.cfg.apiTimeout())
	return err
}

func (c *apiClient) getChatMember(ctx context.Context, chatID, userID int64) (chatMember, error) {
	return callTelegram[chatMember](c, ctx, "getChatMember", getChatMemberRequest{ChatID: chatID, UserID: userID}, c.cfg.apiTimeout())
}

func (c *apiClient) getFile(ctx context.Context, fileID string) (fileInfo, error) {
	return callTelegram[fileInfo](c, ctx, "getFile", getFileRequest{FileID: fileID}, c.cfg.apiTimeout())
}

func (c *apiClient) fileURL(filePath string) string {
	return strings.TrimRight(c.cfg.FileBaseURL, "/") + "/bot" + c.token + "/" + strings.TrimLeft(filePath, "/")
}

func (c *apiClient) downloadFile(ctx context.Context, filePath string) ([]byte, error) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, c.cfg.apiTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileURL(filePath), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, redactTelegramError(err, c.token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram file download failed: http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c *apiClient) sendPhoto(ctx context.Context, chatID int64, source mediaSource, replyTo int64) (sentMessage, error) {
	return c.sendMedia(ctx, "sendPhoto", chatID, "photo", source, replyTo)
}

func (c *apiClient) sendDocument(ctx context.Context, chatID int64, source mediaSource, replyTo int64) (sentMessage, error) {
	return c.sendMedia(ctx, "sendDocument", chatID, "document", source, replyTo)
}

func (c *apiClient) sendMedia(ctx context.Context, method string, chatID int64, field string, source mediaSource, replyTo int64) (sentMessage, error) {
	if value := strings.TrimSpace(source.URL); value != "" {
		return c.sendMediaByReference(ctx, method, chatID, field, value, replyTo)
	}
	if value := strings.TrimSpace(source.Path); delivery.IsHTTPMediaSource(value) {
		return c.sendMediaByReference(ctx, method, chatID, field, value, replyTo)
	}
	body, contentType, err := multipartMediaBody(chatID, field, source, replyTo)
	if err != nil {
		return sentMessage{}, err
	}
	return c.doMultipart(ctx, method, body, contentType)
}

func (c *apiClient) sendMediaByReference(ctx context.Context, method string, chatID int64, field string, value string, replyTo int64) (sentMessage, error) {
	body := map[string]any{"chat_id": chatID, field: value}
	if replyTo != 0 {
		body["reply_to_message_id"] = replyTo
	}
	return callTelegram[sentMessage](c, ctx, method, body, c.cfg.apiTimeout())
}

func callTelegram[T any](c *apiClient, ctx context.Context, method string, in any, timeout time.Duration) (T, error) {
	var zero T
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return zero, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(method), body)
	if err != nil {
		return zero, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return zero, redactTelegramError(err, c.token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return zero, err
	}
	return decodeTelegramResponse[T](method, resp.StatusCode, data)
}

func (c *apiClient) doMultipart(ctx context.Context, method string, body *bytes.Buffer, contentType string) (sentMessage, error) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, c.cfg.apiTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(method), body)
	if err != nil {
		return sentMessage{}, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return sentMessage{}, redactTelegramError(err, c.token)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return sentMessage{}, err
	}
	return decodeTelegramResponse[sentMessage](method, resp.StatusCode, data)
}

type redactedTelegramError struct {
	text string
	err  error
}

func (e redactedTelegramError) Error() string { return e.text }

func (e redactedTelegramError) Unwrap() error { return e.err }

func redactTelegramError(err error, token string) error {
	if err == nil {
		return nil
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return err
	}
	text := strings.ReplaceAll(err.Error(), "/bot"+token+"/", "/bot<redacted>/")
	text = strings.ReplaceAll(text, token, "<redacted>")
	if text == err.Error() {
		return err
	}
	return redactedTelegramError{text: text, err: err}
}

func decodeTelegramResponse[T any](method string, status int, data []byte) (T, error) {
	var zero T
	var out apiResponse[T]
	if err := json.Unmarshal(data, &out); err != nil {
		return zero, fmt.Errorf("decode telegram %s response: %w", method, err)
	}
	if status < 200 || status >= 300 || !out.OK {
		desc := strings.TrimSpace(out.Description)
		if desc == "" {
			desc = strings.TrimSpace(string(data))
		}
		return zero, fmt.Errorf("telegram api %s failed: http %d code %d: %s", method, status, out.ErrorCode, desc)
	}
	return out.Result, nil
}

func (c *apiClient) apiURL(method string) string {
	return strings.TrimRight(c.cfg.APIBaseURL, "/") + "/bot" + c.token + "/" + strings.TrimLeft(method, "/")
}

func isTelegramParseError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "parse") || strings.Contains(text, "can't parse") || strings.Contains(text, "entities")
}

func multipartMediaBody(chatID int64, field string, source mediaSource, replyTo int64) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return nil, "", err
	}
	if replyTo != 0 {
		if err := writer.WriteField("reply_to_message_id", strconv.FormatInt(replyTo, 10)); err != nil {
			return nil, "", err
		}
	}
	name := strings.TrimSpace(source.Name)
	if name == "" && strings.TrimSpace(source.Path) != "" {
		name = filepath.Base(source.Path)
	}
	if name == "" {
		name = "file"
	}
	part, err := writer.CreateFormFile(field, name)
	if err != nil {
		return nil, "", err
	}
	if len(source.Data) > 0 {
		if _, err := part.Write(source.Data); err != nil {
			return nil, "", err
		}
	} else {
		path := strings.TrimSpace(source.Path)
		if path == "" {
			return nil, "", fmt.Errorf("telegram media source is empty")
		}
		if delivery.IsBase64MediaSource(path) {
			data, err := base64.StdEncoding.DecodeString(path[len("base64://"):])
			if err != nil {
				return nil, "", fmt.Errorf("decode telegram media base64 source: %w", err)
			}
			if _, err := part.Write(data); err != nil {
				return nil, "", err
			}
		} else {
			path, err := delivery.FileURIToPath(path)
			if err != nil {
				return nil, "", err
			}
			file, err := os.Open(path)
			if err != nil {
				return nil, "", fmt.Errorf("open media source %q: %w", path, err)
			}
			defer file.Close()
			if _, err := io.Copy(part, file); err != nil {
				return nil, "", err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return &buf, writer.FormDataContentType(), nil
}
