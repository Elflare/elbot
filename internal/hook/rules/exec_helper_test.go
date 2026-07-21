package rules

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var execHelperInitFrame map[string]any

var execHelperEventFrame map[string]any

var execHelperEventID string

func TestExecHelperProcess(t *testing.T) {
	marker := -1
	for i := 0; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--" && os.Args[i+1] == "elbot-exec-helper" {
			marker = i + 2
			break
		}
	}
	if marker == -1 {
		return
	}
	if marker >= len(os.Args) {
		os.Exit(2)
	}
	if err := execHelperHandshake(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Args[marker] {
	case "print":
		writeProtocolTestOutput(strings.Join(os.Args[marker+1:], " "))
	case "argv":
		data, _ := json.Marshal(os.Args[marker+1:])
		writeProtocolTestOutput(string(data))
	case "env":
		if marker+1 >= len(os.Args) {
			os.Exit(2)
		}
		writeProtocolTestOutput(os.Getenv(os.Args[marker+1]))
	case "done-message":
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok", "message": map[string]string{"text": "clean"}})
	case "done-empty-message":
		writeProtocolTestResult(map[string]any{"status": "completed", "message": map[string]string{"text": ""}})
	case "done-segments":
		writeProtocolTestResult(map[string]any{"status": "completed", "message": map[string]any{"segments": []map[string]any{
			{"type": "text", "text": "截图完成"},
			{"type": "image", "base64": "aGVsbG8=", "mime_type": "image/png", "name": "result.png"},
		}}})
	case "done-result":
		result := "ok"
		if marker+1 < len(os.Args) {
			result = os.Args[marker+1]
		}
		writeProtocolTestResult(map[string]any{"status": "completed", "result": result})
	case "unmatched":
		writeProtocolTestResult(map[string]any{"status": "completed", "matched": false, "outputs": []map[string]any{{"kind": "text", "text": "should not survive"}}})
	case "pass-true":
		writeProtocolTestResult(map[string]any{"status": "completed", "pass_through": true})
	case "pass-false":
		writeProtocolTestResult(map[string]any{"status": "completed", "pass_through": false})
	case "stderr-success":
		fmt.Fprintln(os.Stderr, "plugin diagnostic")
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok"})
	case "stderr-no-newline":
		fmt.Fprint(os.Stderr, "partial diagnostic")
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "ok"})
	case "stdin":
		data, _ := json.Marshal(execHelperEventFrame)
		writeProtocolTestOutput(string(data))
	case "base64-output":
		if marker+1 >= len(os.Args) {
			os.Exit(2)
		}
		var size int
		if _, err := fmt.Sscanf(os.Args[marker+1], "%d", &size); err != nil || size < 0 {
			os.Exit(2)
		}
		encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), size))
		writeProtocolTestResult(map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "image", "base64": encoded}}})
	case "read":
		if marker+1 >= len(os.Args) {
			os.Exit(2)
		}
		data, err := os.ReadFile(os.Args[marker+1])
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(1)
		}
		writeProtocolTestOutput(string(data))
	case "crash-stderr":
		fmt.Fprintln(os.Stderr, "script exploded")
		os.Exit(7)
	case "missing-done-stderr":
		fmt.Fprintln(os.Stderr, "wrote stderr before clean exit")
	case "invalid-json-stderr":
		fmt.Fprintln(os.Stderr, "bad json stderr")
		fmt.Fprintln(os.Stdout, `{not json`)
	case "unknown-frame":
		fmt.Fprintln(os.Stdout, `{"type":"mystery"}`)
	case "bad-output":
		fmt.Fprintln(os.Stdout, `{"type":"output","output":{"kind":"text","text":"wrong field"}}`)
	case "plugin-error-frame":
		fmt.Fprintln(os.Stdout, `{"type":"error","error":"plugin said no"}`)
	case "stderr-no-newline-crash":
		fmt.Fprint(os.Stderr, "partial crash diagnostic")
		os.Exit(8)
	case "many-stderr":
		for i := 0; i < 25; i++ {
			fmt.Fprintf(os.Stderr, "stderr line %02d\n", i)
		}
		os.Exit(9)
	case "sleep-stderr":
		fmt.Fprintln(os.Stderr, "waiting forever")
		time.Sleep(5 * time.Second)
	case "close-stdin-after-request":
		_ = os.Stdin.Close()
		time.Sleep(100 * time.Millisecond)
		fmt.Fprintln(os.Stdout, `{"type":"request","id":"plugin:reply","method":"message.get_reply"}`)
		time.Sleep(5 * time.Second)
	case "shared-state":
		reader := bufio.NewReader(os.Stdin)
		getResult, err := execHelperHostRequest(reader, "plugin:get", "shared.get", map[string]any{"key": "worker-data"})
		if err != nil || getResult["found"] != true || getResult["value"] != "from-worker" {
			fmt.Fprint(os.Stderr, firstNonEmpty(errorText(err), "worker-data not found"))
			os.Exit(11)
		}
		if _, err := execHelperHostRequest(reader, "plugin:set", "shared.set", map[string]any{"key": "once-data", "value": 1, "ttl_seconds": 0}); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(11)
		}
		casResult, err := execHelperHostRequest(reader, "plugin:cas", "shared.compare_and_swap", map[string]any{"key": "once-data", "expected": 1, "value": 2, "ttl_seconds": 0})
		if err != nil || casResult["swapped"] != true {
			fmt.Fprint(os.Stderr, firstNonEmpty(errorText(err), "once-data CAS failed"))
			os.Exit(11)
		}
		writeProtocolTestResult(map[string]any{"status": "completed", "result": "shared-ok"})
	case "signal-and-wait":
		if marker+2 >= len(os.Args) {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[marker+1], []byte("ready"), 0o644); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(1)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(os.Args[marker+2]); err == nil {
				writeProtocolTestResult(map[string]any{"status": "completed", "result": "ready"})
				break
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "timed out waiting for peer marker")
				os.Exit(10)
			}
			time.Sleep(10 * time.Millisecond)
		}
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

func execHelperHandshake() error {
	reader := bufio.NewReader(os.Stdin)
	initLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	var init map[string]any
	if err := json.Unmarshal([]byte(initLine), &init); err != nil {
		return err
	}
	if init["type"] != "request" || init["method"] != "system.init" {
		return fmt.Errorf("unexpected init frame %#v", init)
	}
	execHelperInitFrame = init
	initID, _ := init["id"].(string)
	fmt.Fprintf(os.Stdout, `{"type":"response","id":%q,"ok":true,"result":{}}`+"\n", initID)
	eventLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(eventLine), &event); err != nil {
		return err
	}
	if event["type"] != "request" || event["method"] != "event.handle" {
		return fmt.Errorf("unexpected event frame %#v", event)
	}
	execHelperEventID, _ = event["id"].(string)
	execHelperEventFrame = event
	return nil
}

func execHelperHostRequest(reader *bufio.Reader, id, method string, params any) (map[string]any, error) {
	request, err := json.Marshal(map[string]any{"type": "request", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(os.Stdout, string(request)); err != nil {
		return nil, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var response struct {
		Type   string         `json:"type"`
		ID     string         `json:"id"`
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return nil, err
	}
	if response.Type != "response" || response.ID != id || !response.OK {
		return nil, fmt.Errorf("host response %q failed: %s", id, response.Error)
	}
	return response.Result, nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func writeProtocolTestResult(result any) {
	data, _ := json.Marshal(map[string]any{"type": "response", "id": execHelperEventID, "ok": true, "result": result})
	fmt.Fprintln(os.Stdout, string(data))
}

func writeProtocolTestOutput(text string) {
	writeProtocolTestResult(map[string]any{"status": "completed", "outputs": []map[string]any{{"kind": "text", "text": text}}})
}

func execHelperCommand(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=TestExecHelperProcess", "--", "elbot-exec-helper"}, args...)
}
