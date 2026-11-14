package workspace

import (
	"encoding/json"
	"testing"
)

func TestDecodeStateDefaultsAgentToUserSync(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "empty metadata", raw: "", want: true},
		{name: "missing preference", raw: `{"other":1}`, want: true},
		{name: "explicitly enabled", raw: `{"workspace_agent_to_user":true}`, want: true},
		{name: "explicitly disabled", raw: `{"workspace_agent_to_user":false}`, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := DecodeState(tc.raw)
			if state.AgentToUser != tc.want || state.UserToAgent {
				t.Fatalf("state=%+v", state)
			}
		})
	}
}

func TestUpdateStatePersistsExplicitAgentToUserChoice(t *testing.T) {
	updated := UpdateState(`{"keep":1}`, func(state *State) { state.AgentToUser = false })
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(updated), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["workspace_agent_to_user"]) != "false" || string(fields["keep"]) != "1" {
		t.Fatalf("metadata fields=%s", updated)
	}
	state := DecodeState(updated)
	if state.AgentToUser {
		t.Fatalf("disabled state=%+v", state)
	}

	updated = UpdateState(updated, func(state *State) { state.AgentToUser = true })
	state = DecodeState(updated)
	if !state.AgentToUser {
		t.Fatalf("enabled state=%+v", state)
	}
}

func TestUpdateStateUsesDefaultForNewAgentDirectory(t *testing.T) {
	updated := UpdateState(`{"keep":1}`, func(state *State) { state.SetAgentDir("/agent") })
	state := DecodeState(updated)
	if !state.AgentToUser || state.Dir != "/agent" || state.UserDir != "/agent" {
		t.Fatalf("state=%+v", state)
	}
}
