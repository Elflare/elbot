package modelmgr

import (
	"fmt"

	"elbot/internal/llm"
)

// CanSwitch compares saved material ownership with a configured target. It
// does not resolve models, mutate selections or inspect session state.
func CanSwitch(source, target llm.Origin) error {
	if target.APIType == "" || target.Provider == "" {
		return fmt.Errorf("目标模型缺少协议或厂商归属")
	}
	if source.APIType == "" {
		return nil
	}
	if source.APIType == target.APIType && (source.APIType == llm.APITypeChat || (source.Provider != "" && source.Provider == target.Provider)) {
		return nil
	}
	return fmt.Errorf("会话上下文 %s/%s 与目标模型 %s/%s 不兼容，请新建会话或切回兼容模型", source.Provider, source.APIType, target.Provider, target.APIType)
}
