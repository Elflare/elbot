package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	streamPrefixBytes  = 8 * 1024
	maxStreamLineBytes = 8 * 1024 * 1024
)

type Frame struct {
	Event string
	Data  []byte
	Err   error
}

// SSE owns the body and its scanner; Close also wakes blocked event producers.
type SSE struct {
	Frames <-chan Frame
	cancel context.CancelFunc
	body   io.ReadCloser
	once   sync.Once
}

func (s *SSE) Close() { s.once.Do(func() { s.cancel(); _ = s.body.Close() }) }

func (c *Client) OpenSSE(ctx context.Context, resp *http.Response) (*SSE, error) {
	body, err := prepareStreamBody(ctx, resp, c.options.FirstChunkTimeout)
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	frames := make(chan Frame)
	s := &SSE{Frames: frames, cancel: cancel, body: body}
	go func() { defer close(frames); defer s.Close(); readFrames(streamCtx, body, frames, c.options) }()
	return s, nil
}

type streamLine struct {
	line string
	err  error
}

func scanStreamLines(ctx context.Context, body io.ReadCloser, lines chan<- streamLine) {
	defer close(lines)
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxStreamLineBytes)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		case lines <- streamLine{line: scanner.Text()}:
		}
	}
	if err := scanner.Err(); err != nil {
		if strings.Contains(err.Error(), "token too long") {
			err = fmt.Errorf("SSE line exceeds maximum size of %d bytes", maxStreamLineBytes)
		}
		select {
		case <-ctx.Done():
		case lines <- streamLine{err: err}:
		}
	}
}

func resetTimer(timer *time.Timer, timeout time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(timeout)
}
func prepareStreamBody(ctx context.Context, resp *http.Response, timeout time.Duration) (io.ReadCloser, error) {
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "text/html" {
		return nil, errors.New("上游返回 HTML 而不是 API 响应，请检查 API Base URL、代理或网关")
	}
	reader := bufio.NewReaderSize(resp.Body, streamPrefixBytes)
	type readResult struct {
		prefix []byte
		err    error
	}
	result := make(chan readResult, 1)
	go func() {
		prefix, err := reader.ReadSlice('\n')
		result <- readResult{prefix: append([]byte(nil), prefix...), err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var read readResult
	select {
	case <-ctx.Done():
		_ = resp.Body.Close()
		return nil, ctx.Err()
	case <-timer.C:
		_ = resp.Body.Close()
		return nil, fmt.Errorf("LLM first stream chunk timeout after %s", timeout)
	case read = <-result:
	}
	if read.err != nil && read.err != io.EOF && read.err != bufio.ErrBufferFull {
		return nil, fmt.Errorf("read upstream response prefix: %w", read.err)
	}
	prefix := read.prefix

	if looksLikeHTML(prefix) {
		return nil, errors.New("上游返回 HTML 而不是 API 响应，请检查 API Base URL、代理或网关")
	}
	isSSE := looksLikeSSE(prefix) || (len(bytes.TrimSpace(prefix)) == 0 && mediaType == "text/event-stream")
	if !isSSE {
		return nil, fmt.Errorf("上游返回非 SSE API 响应（Content-Type=%q）：%s", contentType, responseSummary(prefix))
	}
	return &prefixedReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), reader), closer: resp.Body}, nil
}

func looksLikeHTML(prefix []byte) bool {
	text := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(string(prefix), "\ufeff")))
	for _, marker := range []string{"<!doctype html", "<html", "<head", "<body"} {
		if strings.HasPrefix(text, marker) {
			return true
		}
	}
	return false
}

func looksLikeSSE(prefix []byte) bool {
	for _, line := range strings.Split(string(prefix), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "retry:") || strings.HasPrefix(line, ":")
	}
	return false
}

func responseSummary(prefix []byte) string {
	const maxSummaryBytes = 256
	text := strings.Join(strings.Fields(string(prefix)), " ")
	for _, marker := range []string{";base64,", "base64://"} {
		if index := strings.Index(strings.ToLower(text), marker); index >= 0 {
			text = text[:index+len(marker)] + "[redacted]"
		}
	}
	for _, marker := range []string{"authorization:", "api_key=", "api-key="} {
		if index := strings.Index(strings.ToLower(text), marker); index >= 0 {
			text = text[:index+len(marker)] + "[redacted]"
		}
	}
	if len(text) > maxSummaryBytes {
		text = text[:maxSummaryBytes] + "..."
	}
	if text == "" {
		return "响应内容为空或无法识别"
	}
	return text
}

type prefixedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *prefixedReadCloser) Close() error {
	return r.closer.Close()
}

// SafeSummary bounds and redacts upstream error text without decoding its API schema.
func SafeSummary(data []byte) string { return responseSummary(data) }
func LooksLikeHTML(data []byte) bool { return looksLikeHTML(data) }

func readFrames(ctx context.Context, body io.ReadCloser, out chan<- Frame, options Options) {
	lines := make(chan streamLine, 1)
	go scanStreamLines(ctx, body, lines)
	timer := time.NewTimer(options.FirstChunkTimeout)
	defer timer.Stop()
	seenData := false
	var event string
	var data []string
	size := 0
	send := func(f Frame) bool {
		select {
		case out <- f:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if seenData {
				send(Frame{Err: fmt.Errorf("LLM stream idle timeout after %s", options.StreamIdleTimeout)})
			} else {
				send(Frame{Err: fmt.Errorf("LLM first stream chunk timeout after %s", options.FirstChunkTimeout)})
			}
			return
		case line, ok := <-lines:
			if !ok {
				send(Frame{Err: io.EOF})
				return
			}
			if line.err != nil {
				send(Frame{Err: line.err})
				return
			}
			value := strings.TrimPrefix(line.line, "\ufeff")
			if value == "" {
				if len(data) > 0 {
					if !send(Frame{Event: event, Data: []byte(strings.Join(data, "\n"))}) {
						return
					}
				}
				event = ""
				data = nil
				size = 0
				continue
			}
			if strings.HasPrefix(value, ":") {
				continue
			}
			key, value, _ := strings.Cut(value, ":")
			value = strings.TrimPrefix(value, " ")
			switch key {
			case "event":
				event = value
			case "data":
				size += len(value) + 1
				if size > maxStreamLineBytes {
					send(Frame{Err: fmt.Errorf("SSE frame exceeds maximum size of %d bytes", maxStreamLineBytes)})
					return
				}
				data = append(data, value)
				seenData = true
				resetTimer(timer, options.StreamIdleTimeout)
			}
		}
	}
}
