// token.go 负责访问令牌的获取、缓存与刷新，供 HTTP 请求和网关鉴权使用。

package qqofficial

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type tokenSource struct {
	cfg    Config
	client *http.Client

	refreshGate chan struct{} // 串行访问缓存与刷新；等待资格可由调用方取消。
	token       string        // 最近成功获取的访问令牌。
	expiresAt   time.Time     // 缓存令牌的过期时间。
}

func newTokenSource(cfg Config, client *http.Client) *tokenSource {
	return &tokenSource{cfg: cfg, client: client, refreshGate: make(chan struct{}, 1)}
}

func (s *tokenSource) tokenValue(ctx context.Context) (string, error) {
	return s.refresh(ctx, false)
}

func (s *tokenSource) refresh(ctx context.Context, force bool) (string, error) {
	select {
	case s.refreshGate <- struct{}{}:
		defer func() { <-s.refreshGate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	// 资格和取消可能同时就绪；已取消调用不得读取或清除缓存。
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !force && s.token != "" && time.Until(s.expiresAt) > time.Minute {
		return s.token, nil
	}
	if force {
		s.token = ""
		s.expiresAt = time.Time{}
	}
	body, err := json.Marshal(accessTokenRequest{AppID: strings.TrimSpace(s.cfg.AppID), ClientSecret: s.cfg.ClientSecret})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, defaultTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get qqofficial access token: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("get qqofficial access token: http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var out accessTokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("decode qqofficial access token: %w", err)
	}
	if out.Code != 0 {
		return "", fmt.Errorf("get qqofficial access token: code %d: %s", out.Code, out.Message)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("get qqofficial access token: empty token")
	}
	expires, err := parseExpiresIn(out.ExpiresIn)
	if err != nil {
		return "", fmt.Errorf("decode qqofficial token expires_in: %w", err)
	}
	if expires <= 0 {
		expires = 7200
	}
	s.token = strings.TrimSpace(out.AccessToken)
	s.expiresAt = time.Now().Add(time.Duration(expires) * time.Second)
	return s.token, nil
}

func (s *tokenSource) forceRefresh(ctx context.Context) (string, error) {
	return s.refresh(ctx, true)
}

func parseExpiresIn(value any) (int64, error) {
	switch v := value.(type) {
	case nil:
		return 0, nil
	case json.Number:
		return parseExpiresText(v.String())
	case string:
		return parseExpiresText(v)
	case float64:
		return int64(v), nil
	default:
		return parseExpiresText(fmt.Sprint(v))
	}
}

func parseExpiresText(text string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	if unquoted, err := strconv.Unquote(text); err == nil {
		text = unquoted
	}
	return strconv.ParseInt(text, 10, 64)
}
