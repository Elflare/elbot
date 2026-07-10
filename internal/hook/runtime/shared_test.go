package runtime

import (
	"encoding/json"
	"testing"
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

func TestSharedStateRejectsInvalidKeysAndValues(t *testing.T) {
	state := NewSharedState()
	for _, tc := range []struct {
		key   string
		value json.RawMessage
	}{
		{key: "missing-namespace", value: json.RawMessage(`1`)},
		{key: "/empty", value: json.RawMessage(`1`)},
		{key: "demo/value", value: json.RawMessage(`not-json`)},
	} {
		if err := state.Set(tc.key, tc.value); err == nil {
			t.Fatalf("Set(%q, %s) succeeded", tc.key, tc.value)
		}
	}
}

func TestConfigValidateRequiresExplicitLifecycle(t *testing.T) {
	config := Config{
		Stateful:               true,
		Command:                "hook.exe",
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
	config.Restart.Strategy = ""
	if err := config.Validate(); err == nil {
		t.Fatal("Validate accepted missing restart strategy")
	}
}
