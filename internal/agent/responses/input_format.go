package responses

import (
	"context"
	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

const inputFormatVersion = 1
const inputFormatKey = "responses_input_version"

func sessionInputVersion(row *storage.Session) (int, error) {
	fields, err := storage.DecodeSessionMetadata(row.Metadata)
	if err != nil {
		return 0, err
	}
	var version int
	if raw, ok := fields[inputFormatKey]; ok {
		if err := json.Unmarshal(raw, &version); err != nil {
			return 0, fmt.Errorf("读取 Responses 输入格式: %w", err)
		}
	}
	return version, nil
}

func checkInputFormat(row *storage.Session) error {
	version, err := sessionInputVersion(row)
	if err != nil {
		return err
	}
	if version != inputFormatVersion {
		return incompatibleInputFormat()
	}
	return nil
}

func incompatibleInputFormat() error {
	return fmt.Errorf("Responses 会话使用旧版或不支持的工具输入格式，请新建会话")
}

// Stamp new sessions after execution admission and before their first input.
// Fork/compact roots carry their own version, so they survive source deletion.
func (s *turnState) ensureInputFormat(ctx context.Context) error {
	version, err := sessionInputVersion(s.session)
	if err != nil {
		return err
	}
	if version == inputFormatVersion {
		return nil
	}
	if version != 0 {
		return incompatibleInputFormat()
	}
	locked, release, err := s.route.Messages.Gate.Enter(ctx, s.session.ID)
	if err != nil {
		return err
	}
	defer release()
	row, err := s.route.View.Sessions.Mutate(locked, s.session.ID, func(row *storage.Session) error {
		version, err := sessionInputVersion(row)
		if err != nil {
			return err
		}
		if version != 0 && version != inputFormatVersion {
			return incompatibleInputFormat()
		}
		fields, err := storage.DecodeSessionMetadata(row.Metadata)
		if err != nil {
			return err
		}
		if err := fields.Set(inputFormatKey, inputFormatVersion); err != nil {
			return err
		}
		row.Metadata, err = fields.Encode()
		return err
	})
	if err == nil {
		*s.session = *row
	}
	return err
}

type seedInputs struct {
	Version int        `json:"version"`
	Items   []api.Item `json:"items"`
}

func decodeSeedInputs(seed *storage.NativeSeed) ([]api.Item, error) {
	var saved seedInputs
	if err := json.Unmarshal([]byte(seed.ItemsJSON), &saved); err != nil {
		return nil, fmt.Errorf("%w: %v", incompatibleInputFormat(), err)
	}
	if saved.Version != inputFormatVersion || saved.Items == nil {
		return nil, incompatibleInputFormat()
	}
	return saved.Items, nil
}
