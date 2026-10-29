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

// InheritOrigin 只继承材料归属，不带入源会话的其他元数据或运行状态。
func InheritOrigin(source *storage.Session, metadata string) (string, error) {
	origin, known, err := Origin(source)
	if err != nil {
		return "", err
	}
	fields, err := storage.DecodeSessionMetadata(metadata)
	if err != nil {
		return "", err
	}
	if known {
		if err := fields.Set(originMetadataKey, origin); err != nil {
			return "", err
		}
	} else {
		delete(fields, originMetadataKey)
	}
	return fields.Encode()
}
