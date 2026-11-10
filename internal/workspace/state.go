// 编解码 Session 中的工作目录与说明文件提示状态。
package workspace

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type State struct {
	// Dir 是 Session 保存的工作目录，不包含调用级路径限制。
	Dir string `json:"workspace_dir,omitempty"`
	// UserDir 是前端独立选择的目录，不影响 Agent，除非对应同步方向开启。
	UserDir string `json:"user_workspace_dir,omitempty"`
	// AgentToUser 表示 Agent workspace 变化时同步更新用户 workspace。
	AgentToUser bool `json:"workspace_agent_to_user,omitempty"`
	// UserToAgent 表示用户 workspace 变化时同步更新 Agent workspace。
	UserToAgent bool `json:"workspace_user_to_agent,omitempty"`
	// AgentNoticeDirs 记录已向模型附带工作约定的目录，避免同一目录重复提示。
	AgentNoticeDirs []string `json:"workspace_agent_notice_dirs,omitempty"`
}

func DecodeState(raw string) (State, error) {
	fields := map[string]json.RawMessage{}
	if raw != "" {
		json.Unmarshal([]byte(raw), &fields)
	}
	return decodeWorkspaceState(fields)
}

func UpdateState(raw string, update func(*State)) (string, error) {
	fields := map[string]json.RawMessage{}
	if raw != "" {
		json.Unmarshal([]byte(raw), &fields)
	}
	state, err := decodeWorkspaceState(fields)
	if err != nil {
		return "", err
	}
	before := state
	update(&state)
	writeWorkspaceState(fields, before, state)
	if len(fields) == 0 {
		return "", nil
	}
	encoded, _ := json.Marshal(fields)
	return string(encoded), nil
}

func decodeWorkspaceState(fields map[string]json.RawMessage) (State, error) {
	state := State{AgentToUser: true}
	for key, raw := range fields {
		var target any
		switch key {
		case "workspace_dir":
			target = &state.Dir
		case "user_workspace_dir":
			target = &state.UserDir
		case "workspace_agent_to_user":
			target = &state.AgentToUser
		case "workspace_user_to_agent":
			target = &state.UserToAgent
		case "workspace_agent_notice_dirs":
			target = &state.AgentNoticeDirs
		default:
			continue
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return State{}, fmt.Errorf("decode workspace field %s: %w", key, err)
		}
	}
	state.Dir = strings.TrimSpace(state.Dir)
	state.UserDir = strings.TrimSpace(state.UserDir)
	return state, nil
}

func writeWorkspaceState(fields map[string]json.RawMessage, before, after State) {
	if before.Dir != after.Dir {
		setString(fields, "workspace_dir", after.Dir)
	}
	if before.UserDir != after.UserDir {
		setString(fields, "user_workspace_dir", after.UserDir)
	}
	if before.AgentToUser != after.AgentToUser {
		fields["workspace_agent_to_user"], _ = json.Marshal(after.AgentToUser)
	}
	if before.UserToAgent != after.UserToAgent {
		setBool(fields, "workspace_user_to_agent", after.UserToAgent)
	}
	if !slices.Equal(before.AgentNoticeDirs, after.AgentNoticeDirs) {
		if len(after.AgentNoticeDirs) == 0 {
			delete(fields, "workspace_agent_notice_dirs")
		} else {
			fields["workspace_agent_notice_dirs"], _ = json.Marshal(after.AgentNoticeDirs)
		}
	}
}

func setString(fields map[string]json.RawMessage, key, value string) {
	if value == "" {
		delete(fields, key)
	} else {
		fields[key], _ = json.Marshal(value)
	}
}

func setBool(fields map[string]json.RawMessage, key string, value bool) {
	if value {
		fields[key] = json.RawMessage("true")
	} else {
		delete(fields, key)
	}
}

func (s *State) SetAgentDir(dir string) {
	s.Dir = strings.TrimSpace(dir)
	if s.AgentToUser {
		s.UserDir = s.Dir
	}
}

func (s *State) ClearAgentDir(dir string, mark bool) {
	s.Dir = ""
	if s.AgentToUser {
		s.UserDir = ""
	}
	if mark {
		s.MarkAgentNoticeDir(dir)
	}
}

func (s *State) ClearAgentDirIfMatches(dir string) bool {
	if s.Dir != dir {
		return false
	}
	s.ClearAgentDir("", false)
	return true
}

func (s *State) EnsureAgentDir(dir string) {
	if s.Dir == "" {
		s.SetAgentDir(dir)
	}
}

func (s *State) SetUserDir(dir string) {
	s.UserDir = strings.TrimSpace(dir)
	if s.UserToAgent {
		s.Dir = s.UserDir
	}
}

func (s *State) SetSync(agentToUser, userToAgent bool, agentDir string) {
	newAgentToUser := agentToUser && !s.AgentToUser
	newUserToAgent := userToAgent && !s.UserToAgent
	s.AgentToUser, s.UserToAgent = agentToUser, userToAgent
	if newAgentToUser {
		s.UserDir = agentDir
	}
	if newUserToAgent {
		s.Dir = s.UserDir
	}
}

func (s *State) MarkAgentNoticeDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir != "" && !slices.Contains(s.AgentNoticeDirs, dir) {
		s.AgentNoticeDirs = append(s.AgentNoticeDirs, dir)
	}
}

func (s State) HasAgentNoticeDir(dir string) bool {
	return slices.Contains(s.AgentNoticeDirs, strings.TrimSpace(dir))
}
