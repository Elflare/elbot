package qqofficial

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type apiClient struct {
	cfg    Config
	http   *http.Client
	tokens *tokenSource
}

func newAPIClient(cfg Config) *apiClient {
	h := &http.Client{Timeout: cfg.httpTimeout()}
	return &apiClient{cfg: cfg, http: h, tokens: newTokenSource(cfg, h)}
}

func (c *apiClient) gateway(ctx context.Context) (string, error) {
	if value := strings.TrimSpace(c.cfg.GatewayURL); value != "" {
		return value, nil
	}
	var out gatewayResponse
	if err := c.doJSON(ctx, http.MethodGet, "/gateway", nil, &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.URL) == "" {
		return "", fmt.Errorf("qqofficial gateway url is empty")
	}
	return strings.TrimSpace(out.URL), nil
}

func (c *apiClient) sendMessage(ctx context.Context, openID string, msg messageToCreate) (messageResponse, error) {
	var out messageResponse
	path := "/v2/users/" + url.PathEscape(openID) + "/messages"
	if err := c.doJSON(ctx, http.MethodPost, path, msg, &out); err != nil {
		return messageResponse{}, err
	}
	return out, nil
}

func (c *apiClient) uploadFile(ctx context.Context, openID string, fileType int, source preparedSource) (uploadFileResponse, error) {
	reqBody := uploadFileRequest{FileType: fileType, SrvSendMsg: false}
	if source.URL != "" {
		reqBody.URL = source.URL
	} else if len(source.Data) > 0 {
		reqBody.FileData = base64.StdEncoding.EncodeToString(source.Data)
	} else {
		return uploadFileResponse{}, fmt.Errorf("qqofficial media source is empty")
	}
	var out uploadFileResponse
	path := "/v2/users/" + url.PathEscape(openID) + "/files"
	if err := c.doJSON(ctx, http.MethodPost, path, reqBody, &out); err != nil {
		return uploadFileResponse{}, err
	}
	if strings.TrimSpace(out.FileInfo) == "" {
		return uploadFileResponse{}, fmt.Errorf("qqofficial upload returned empty file_info")
	}
	return out, nil
}

func (c *apiClient) doJSON(ctx context.Context, method, path string, in any, out any) error {
	return c.doJSONWithRetry(ctx, method, path, in, out, false)
}

func (c *apiClient) doJSONWithRetry(ctx context.Context, method, path string, in any, out any, retried bool) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	reqURL := strings.TrimRight(c.cfg.APIBaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	token, err := c.tokens.tokenValue(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized && !retried {
		if _, err := c.tokens.forceRefresh(ctx); err != nil {
			return err
		}
		return c.doJSONWithRetry(ctx, method, path, in, out, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr apiErrorResponse
		if err := json.Unmarshal(data, &apiErr); err == nil && (apiErr.Code != 0 || apiErr.Message != "") {
			return fmt.Errorf("qqofficial api %s %s: code %d: %s", method, path, apiErr.Code, apiErr.Message)
		}
		return fmt.Errorf("qqofficial api %s %s: http %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode qqofficial api %s %s: %w", method, path, err)
	}
	return nil
}

type preparedSource struct {
	URL  string
	Data []byte
	Name string
}

func prepareSource(urlValue, path string, data []byte) (preparedSource, error) {
	if value := strings.TrimSpace(urlValue); value != "" {
		return preparedSource{URL: value, Name: filepath.Base(value)}, nil
	}
	if len(data) > 0 {
		return preparedSource{Data: data}, nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return preparedSource{}, fmt.Errorf("source path is empty")
	}
	fileData, err := os.ReadFile(path)
	if err != nil {
		return preparedSource{}, fmt.Errorf("read source %q: %w", path, err)
	}
	return preparedSource{Data: fileData, Name: filepath.Base(path)}, nil
}
