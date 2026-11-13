package responses

import (
	"context"
	"elbot/internal/operationresult"

	"encoding/json"
	"fmt"

	api "elbot/internal/llm/responses"
	"elbot/internal/storage"
)

const inputFormatVersion = 2
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

func checkInputFormat(row *storage.Session) (operationresult.Result[struct{}], error) {
	version, err := sessionInputVersion(row)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if version != inputFormatVersion {
		return incompatibleInputFormat(), nil
	}
	return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
}

func incompatibleInputFormat() operationresult.Result[struct{}] {
	return operationresult.Result[struct{}]{Status: operationresult.Incompatible, Message: "Responses 会话使用旧版或不支持的工具输入格式，请新建会话"}
}

// Stamp new sessions after execution admission and before their first input.
// Fork/compact roots carry their own version, so they survive source deletion.
func (s *turnState) ensureInputFormat(ctx context.Context) (operationresult.Result[struct{}], error) {
	version, err := sessionInputVersion(s.session)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if version == inputFormatVersion {
		return operationresult.Result[struct{}]{Status: operationresult.Success}, nil
	}
	if version != 0 {
		return incompatibleInputFormat(), nil
	}
	locked, release, err := s.route.Gate.Enter(ctx, s.session.ID)
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	defer release()
	result := operationresult.Result[struct{}]{Status: operationresult.Success}
	row, err := s.route.SessionRows.Mutate(locked, s.session.ID, func(row *storage.Session) error {
		version, err := sessionInputVersion(row)
		if err != nil {
			return err
		}
		if version != 0 && version != inputFormatVersion {
			result = incompatibleInputFormat()
			return nil
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
	if err != nil {
		return operationresult.Result[struct{}]{}, err
	}
	if result.Status != operationresult.Success {
		return result, nil
	}
	*s.session = *row
	return result, nil
}

type seedInputs struct {
	Version int        `json:"version"`
	Items   []api.Item `json:"items"`
}

func decodeSeedInputs(seed *storage.NativeSeed) (operationresult.Result[[]api.Item], error) {
	var saved seedInputs
	if err := json.Unmarshal([]byte(seed.ItemsJSON), &saved); err != nil {
		return operationresult.Result[[]api.Item]{}, fmt.Errorf("decode Responses seed inputs: %w", err)
	}
	if saved.Version != inputFormatVersion || saved.Items == nil {
		rejected := incompatibleInputFormat()
		return operationresult.Result[[]api.Item]{Status: rejected.Status, Message: rejected.Message}, nil
	}
	return operationresult.Result[[]api.Item]{Status: operationresult.Success, Data: saved.Items}, nil
}
