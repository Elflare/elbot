package runtime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedStateSetGetAndCompareAndSwap(t *testing.T) {
	state := NewSharedState()
	if err := state.Set("weather/latest", json.RawMessage(`{"temp":20}`)); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok := state.Get("weather/latest")
	if !ok || string(value) != `{"temp":20}` {
		t.Fatalf("Get = %s, %v", value, ok)
	}
	swapped, err := state.CompareAndSwap("weather/latest", json.RawMessage(`{"temp":20}`), json.RawMessage(`{"temp":21}`))
	if err != nil || !swapped {
		t.Fatalf("CompareAndSwap = %v, %v", swapped, err)
	}
	swapped, err = state.CompareAndSwap("weather/latest", json.RawMessage(`{"temp":20}`), json.RawMessage(`{"temp":22}`))
	if err != nil || swapped {
		t.Fatalf("stale CompareAndSwap = %v, %v", swapped, err)
	}
	if keys := state.List("weather/"); len(keys) != 1 || keys[0] != "weather/latest" {
		t.Fatalf("List = %#v", keys)
	}
	if !state.Delete("weather/latest") || state.Delete("weather/latest") {
		t.Fatal("Delete did not report expected state")
	}
}

func TestSharedStateHandleRequest(t *testing.T) {
	state := NewSharedState()
	setResult, err := state.HandleRequest("shared.set", json.RawMessage(`{"key":"flat","value":{"count":1},"ttl_seconds":0}`))
	if err != nil || setResult.(map[string]any)["ok"] != true {
		t.Fatalf("shared.set = %#v, %v", setResult, err)
	}
	getResult, err := state.HandleRequest("shared.get", json.RawMessage(`{"key":"flat"}`))
	if err != nil {
		t.Fatalf("shared.get: %v", err)
	}
	get := getResult.(map[string]any)
	if get["found"] != true || string(get["value"].(json.RawMessage)) != `{"count":1}` {
		t.Fatalf("shared.get = %#v", get)
	}
	listResult, err := state.HandleRequest("shared.list", json.RawMessage(`{"prefix":"fl"}`))
	if err != nil {
		t.Fatalf("shared.list: %v", err)
	}
	keys := listResult.(map[string]any)["keys"].([]string)
	if len(keys) != 1 || keys[0] != "flat" {
		t.Fatalf("shared.list = %#v", keys)
	}
	casResult, err := state.HandleRequest("shared.compare_and_swap", json.RawMessage(`{"key":"flat","expected":{"count":1},"value":{"count":2}}`))
	if err != nil || casResult.(map[string]any)["swapped"] != true {
		t.Fatalf("shared.compare_and_swap = %#v, %v", casResult, err)
	}
	deleteResult, err := state.HandleRequest("shared.delete", json.RawMessage(`{"key":"flat"}`))
	if err != nil || deleteResult.(map[string]any)["deleted"] != true {
		t.Fatalf("shared.delete = %#v, %v", deleteResult, err)
	}
	if _, err := state.HandleRequest("shared.unknown", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown shared method succeeded")
	}
}

func TestSharedStateRejectsInvalidKeysAndValues(t *testing.T) {
	state := NewSharedState()
	if err := state.Set("flat-key", json.RawMessage(`1`)); err != nil {
		t.Fatalf("Set flat key: %v", err)
	}
	for _, tc := range []struct {
		key   string
		value json.RawMessage
	}{
		{key: "", value: json.RawMessage(`1`)},
		{key: "   ", value: json.RawMessage(`1`)},
		{key: strings.Repeat("k", maxSharedKeyBytes+1), value: json.RawMessage(`1`)},
		{key: "demo/value", value: json.RawMessage(`not-json`)},
	} {
		if err := state.Set(tc.key, tc.value); err == nil {
			t.Fatalf("Set(%q, %s) succeeded", tc.key, tc.value)
		}
	}
}

func TestSharedStateIdleTTLAndAccessRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	state := NewSharedState()
	state.now = func() time.Time { return now }
	if err := state.SetWithTTL("hot", json.RawMessage(`1`), 10*time.Second); err != nil {
		t.Fatalf("SetWithTTL: %v", err)
	}
	now = now.Add(9 * time.Second)
	if _, ok := state.Get("hot"); !ok {
		t.Fatal("hot key expired before first idle TTL")
	}
	now = now.Add(9 * time.Second)
	if _, ok := state.Get("hot"); !ok {
		t.Fatal("get did not refresh idle TTL")
	}
	now = now.Add(11 * time.Second)
	if _, ok := state.Get("hot"); ok {
		t.Fatal("idle key did not expire")
	}

	if err := state.SetWithTTL("permanent", json.RawMessage(`1`), 0); err != nil {
		t.Fatalf("Set permanent: %v", err)
	}
	now = now.Add(365 * 24 * time.Hour)
	if _, ok := state.Get("permanent"); !ok {
		t.Fatal("zero TTL key expired")
	}
}

func TestSharedStateListAndFailedCASDoNotRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	state := NewSharedState()
	state.now = func() time.Time { return now }
	if err := state.SetWithTTL("cold", json.RawMessage(`1`), 10*time.Second); err != nil {
		t.Fatalf("SetWithTTL: %v", err)
	}
	now = now.Add(9 * time.Second)
	if keys := state.List(""); len(keys) != 1 {
		t.Fatalf("List = %#v", keys)
	}
	swapped, err := state.CompareAndSwapWithTTL("cold", json.RawMessage(`2`), json.RawMessage(`3`), 10*time.Second)
	if err != nil || swapped {
		t.Fatalf("failed CAS = %v, %v", swapped, err)
	}
	now = now.Add(2 * time.Second)
	if _, ok := state.Get("cold"); ok {
		t.Fatal("list or failed CAS refreshed idle TTL")
	}
}

func TestSharedStatePruneExpired(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	state := NewSharedState()
	state.now = func() time.Time { return now }
	if err := state.SetWithTTL("expired", json.RawMessage(`1`), time.Second); err != nil {
		t.Fatalf("Set expired: %v", err)
	}
	if err := state.SetWithTTL("kept", json.RawMessage(`2`), 0); err != nil {
		t.Fatalf("Set kept: %v", err)
	}
	now = now.Add(2 * time.Second)
	if removed := state.PruneExpired(); removed != 1 {
		t.Fatalf("PruneExpired = %d", removed)
	}
	if keys := state.List(""); len(keys) != 1 || keys[0] != "kept" {
		t.Fatalf("remaining keys = %#v", keys)
	}
}

func TestSharedStateEvictsLeastRecentlyUsed(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	state := NewSharedState()
	state.now = func() time.Time { return now }
	state.maxEntries = 3
	for _, key := range []string{"a", "b", "c"} {
		if err := state.SetWithTTL(key, json.RawMessage(`1`), 0); err != nil {
			t.Fatalf("Set %s: %v", key, err)
		}
		now = now.Add(time.Second)
	}
	if _, ok := state.Get("a"); !ok {
		t.Fatal("Get a failed")
	}
	now = now.Add(time.Second)
	if err := state.SetWithTTL("d", json.RawMessage(`1`), 0); err != nil {
		t.Fatalf("Set d: %v", err)
	}
	state.mu.RLock()
	_, hasA := state.items["a"]
	_, hasB := state.items["b"]
	_, hasD := state.items["d"]
	state.mu.RUnlock()
	if !hasA || hasB || !hasD {
		t.Fatalf("LRU contents: a=%v b=%v d=%v", hasA, hasB, hasD)
	}
}

func TestSharedStateSizeLimitCountsKeysAndValues(t *testing.T) {
	state := NewSharedState()
	state.maxEntries = 10
	state.maxSize = 10
	if err := state.SetWithTTL("aaaa", json.RawMessage(`1`), 0); err != nil {
		t.Fatalf("Set aaaa: %v", err)
	}
	if err := state.SetWithTTL("bbbb", json.RawMessage(`2`), 0); err != nil {
		t.Fatalf("Set bbbb: %v", err)
	}
	if err := state.SetWithTTL("cc", json.RawMessage(`3`), 0); err != nil {
		t.Fatalf("Set cc: %v", err)
	}
	state.mu.RLock()
	_, hasOldest := state.items["aaaa"]
	size := state.size
	state.mu.RUnlock()
	if hasOldest || size > state.maxSize {
		t.Fatalf("size eviction: hasOldest=%v size=%d", hasOldest, size)
	}
}

func TestSharedStateConcurrentCompareAndSwap(t *testing.T) {
	state := NewSharedState()
	if err := state.SetWithTTL("counter", json.RawMessage(`0`), 0); err != nil {
		t.Fatalf("Set counter: %v", err)
	}
	const workers = 8
	const increments = 50
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range increments {
				for {
					current, ok := state.Get("counter")
					if !ok {
						t.Error("counter disappeared")
						return
					}
					value, err := strconv.Atoi(string(current))
					if err != nil {
						t.Errorf("parse counter: %v", err)
						return
					}
					next := json.RawMessage(fmt.Sprintf("%d", value+1))
					swapped, err := state.CompareAndSwapWithTTL("counter", current, next, 0)
					if err != nil {
						t.Errorf("CompareAndSwap: %v", err)
						return
					}
					if swapped {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	value, ok := state.Get("counter")
	if !ok || string(value) != strconv.Itoa(workers*increments) {
		t.Fatalf("counter = %s, %v", value, ok)
	}
}

func TestSharedTTLDefaultsAndValidation(t *testing.T) {
	if ttl, err := sharedTTL(nil); err != nil || ttl != 10*time.Minute {
		t.Fatalf("default TTL = %v, %v", ttl, err)
	}
	zero := int64(0)
	if ttl, err := sharedTTL(&zero); err != nil || ttl != 0 {
		t.Fatalf("zero TTL = %v, %v", ttl, err)
	}
	seconds := int64(90)
	if ttl, err := sharedTTL(&seconds); err != nil || ttl != 90*time.Second {
		t.Fatalf("explicit TTL = %v, %v", ttl, err)
	}
	negative := int64(-1)
	if _, err := sharedTTL(&negative); err == nil {
		t.Fatal("negative TTL succeeded")
	}
	tooLarge := maxSharedTTLSeconds + 1
	if _, err := sharedTTL(&tooLarge); err == nil {
		t.Fatal("overflowing TTL succeeded")
	}
}

func TestConfigValidateRequiresExplicitLifecycle(t *testing.T) {
	config := Config{
		Mode:                   ModePersistent,
		Command:                []string{"hook.exe"},
		Cwd:                    ".",
		StartupTimeoutSeconds:  5,
		ShutdownTimeoutSeconds: 5,
		EventTimeoutSeconds:    30,
		MaxWaitSeconds:         60,
		Restart: RestartConfig{
			Strategy:            "on_failure",
			InitialDelaySeconds: 1,
			MaxDelaySeconds:     10,
		},
		ID:  "demo_hook",
		Dir: t.TempDir(),
	}
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	config.Command = []string{""}
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "runtime command is required") {
		t.Fatalf("Validate empty command: %v", err)
	}
	config.Command = []string{"hook.exe"}
	config.Restart.Strategy = ""
	if err := config.Validate(); err == nil {
		t.Fatal("Validate accepted missing restart strategy")
	}
}
