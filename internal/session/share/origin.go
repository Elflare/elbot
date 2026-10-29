// 职责：提供内部组件共用的模型归属解析与继承规则。
// 登记与持久化归 control，派生会话通过本文件复用归属规则。
package share

import (
	"encoding/json"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/storage"
)

const originMetadataKey = "llm_origin"

// Origin 只读取已保存的归属，不查询当前配置或补造未知的提供方。
func Origin(row *storage.Session) (llm.Origin, bool, error) {
	if row == nil {
		return llm.Origin{}, false, nil
	}
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return llm.Origin{}, false, err
	}
	raw, ok := fields[originMetadataKey]
	if !ok {
		return llm.Origin{}, false, nil
	}
	var origin llm.Origin
	if err := json.Unmarshal(raw, &origin); err != nil {
		return llm.Origin{}, false, fmt.Errorf("decode session origin: %w", err)
	}
	if origin.APIType == "" {
		return llm.Origin{}, false, fmt.Errorf("session origin requires a protocol")
	}
	return origin, true, nil
}

// InheritOrigin 将 Origin 已解析的材料归属写入调用方自有字段集合。
// 零值表示来源无归属；不带入源运行状态，最终编码由调用方完成。
func InheritOrigin(origin llm.Origin, fields storage.SessionMetadata) error {
	if origin.APIType == "" {
		delete(fields, originMetadataKey)
		return nil
	}
	return fields.Set(originMetadataKey, origin)
}
