// 职责：登记会话的模型协议归属。
// 通过共享归属规则和会话准入保证首次登记及后续一致性。
package control

import (
	"context"
	"fmt"

	"elbot/internal/llm"
	"elbot/internal/session/share"
	"elbot/internal/storage"
)

func (s *Service) RegisterOrigin(ctx context.Context, id string, target llm.Origin) (*storage.Session, error) {
	if target.APIType == "" || target.Provider == "" {
		return nil, fmt.Errorf("dialogue origin requires a protocol and provider")
	}
	ctx, release, err := s.enterSession(ctx, id)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.deps.Sessions.Mutate(ctx, id, func(row *storage.Session) error {
		current, known, err := share.Origin(row)
		if err != nil {
			return err
		}
		if known {
			if current.APIType != target.APIType {
				return fmt.Errorf("source protocol %q cannot be reinterpreted as %q", current.APIType, target.APIType)
			}
			if current.Provider != "" {
				return nil
			}
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set("llm_origin", target); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	})
}
