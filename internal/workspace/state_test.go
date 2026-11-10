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
			state, err := DecodeState(tc.raw)
			if err != nil || state.AgentToUser != tc.want || state.UserToAgent {
				t.Fatalf("state=%+v err=%v", state, err)
			}
		})
	}
}

func TestUpdateStatePersistsExplicitAgentToUserChoice(t *testing.T) {
	updated, err := UpdateState(`{"keep":1}`, func(state *State) { state.AgentToUser = false })
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(updated), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["workspace_agent_to_user"]) != "false" || string(fields["keep"]) != "1" {
		t.Fatalf("metadata fields=%s", updated)
	}
	state, err := DecodeState(updated)
	if err != nil || state.AgentToUser {
		t.Fatalf("disabled state=%+v err=%v", state, err)
	}

	updated, err = UpdateState(updated, func(state *State) { state.AgentToUser = true })
	if err != nil {
		t.Fatal(err)
	}
	state, err = DecodeState(updated)
	if err != nil || !state.AgentToUser {
		t.Fatalf("enabled state=%+v err=%v", state, err)
	}
}

func TestUpdateStateUsesDefaultForNewAgentDirectory(t *testing.T) {
	updated, err := UpdateState(`{"keep":1}`, func(state *State) { state.SetAgentDir("/agent") })
	if err != nil {
		t.Fatal(err)
	}
	state, err := DecodeState(updated)
	if err != nil || !state.AgentToUser || state.Dir != "/agent" || state.UserDir != "/agent" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
