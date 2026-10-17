package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("ELBOT_SKILL_TEST_HELPER") {
	case "output":
		runOutputTestHelper()
	case "media":
		runMediaTestHelper()
	case "payload":
		var payload map[string]string
		if err := json.NewDecoder(os.Stdin).Decode(&payload); err != nil {
			panic(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"content": payload["input"]})
	case "shared-env":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"content": os.Getenv("ELBOT_GO_SKILL_ENV_TEST")})
	case "environment":
		runEnvironmentTestHelper()
	default:
		os.Exit(m.Run())
	}
	os.Exit(0)
}

func runEnvironmentTestHelper() {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("executed", nil, 0600); err != nil {
		panic(err)
	}
	values := map[string]string{"cwd": cwd}
	for _, name := range []string{"ELBOT_SKILL_ENV_TEST", "ELBOT_SKILL_CONFIG_ONLY", "ELBOT_SKILL_PROCESS_ONLY", "ELBOT_SKILL_LOCAL_ONLY", "ELBOT_SKILL_EMPTY_TEST", "PATH"} {
		values[name] = os.Getenv(name)
	}
	content, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"content": string(content)}); err != nil {
		panic(err)
	}
}

func runOutputTestHelper() {
	mode := os.Args[1]
	switch mode {
	case "plain exact":
		fmt.Print(strings.Repeat("x", 1024*1024))
	case "plain overflow":
		fmt.Print(strings.Repeat("x", 1024*1024+1))
	case "json exact":
		prefix, suffix := "{\"content\":\"", "\"}"
		fmt.Print(prefix + strings.Repeat("x", 1024*1024-len(prefix)-len(suffix)) + suffix)
	case "json overflow":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"content": strings.Repeat("x", 1024*1024), "segments": []any{map[string]string{"type": "file", "path": "missing.txt"}}})
	case "stderr overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("e", 2*1024*1024))
		fmt.Print("done")
	case "failure", "timeout", "canceled":
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			fmt.Print("stdout-prefix" + strings.Repeat("o", 2*1024*1024))
		}()
		go func() {
			defer wg.Done()
			fmt.Fprint(os.Stderr, "stderr-prefix"+strings.Repeat("e", 2*1024*1024))
		}()
		wg.Wait()
		if mode == "failure" {
			os.Exit(7)
		}
		if err := os.WriteFile(os.Args[2], []byte("ready"), 0600); err != nil {
			panic(err)
		}
		time.Sleep(30 * time.Second)
	}
}

func runMediaTestHelper() {
	var path, mode, workspace string
	if len(os.Args) > 1 {
		for i := 1; i+1 < len(os.Args); i += 2 {
			switch os.Args[i] {
			case "--input":
				path = os.Args[i+1]
			case "--mode":
				mode = os.Args[i+1]
			}
		}
		workspace = filepath.Dir(path)
	} else {
		var payload struct {
			MediaInputs []struct{ Path, Base64 string }
		}
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(os.Stdin).Decode(&raw); err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw["media_inputs"], &payload.MediaInputs); err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw["media_workspace"], &workspace); err != nil {
			panic(err)
		}
		if err := json.Unmarshal(raw["mode"], &mode); err != nil {
			panic(err)
		}
		if len(payload.MediaInputs) == 0 || payload.MediaInputs[0].Base64 != "aW5wdXQ=" {
			os.Exit(4)
		}
		path = payload.MediaInputs[0].Path
	}
	if filepath.IsAbs(path) {
		os.Exit(5)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "input" {
		os.Exit(6)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	if mode == "wait" {
		time.Sleep(10 * time.Second)
	}
	output := filepath.Join(workspace, "output.txt")
	if err := os.WriteFile(output, []byte("output"), 0600); err != nil {
		panic(err)
	}
	if mode == "escape" {
		output = "../escape.txt"
	}
	content := "done"
	if mode == "large" {
		content = strings.Repeat("x", 20000)
	}
	fmt.Printf("{\"content\":%q,\"segments\":[{\"type\":\"file\",\"path\":%q}]}", content, filepath.ToSlash(output))
}
