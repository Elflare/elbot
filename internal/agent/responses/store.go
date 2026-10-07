package responses

import (
	"encoding/json"
	"fmt"

	"elbot/internal/storage"
)

type storageState struct {
	Requested bool
	Available bool
}

// Use persisted wire facts so restarting or resuming a failed turn requires no
// separate capability cache. Either side disabling storage rules out chaining.
func exchangeStorage(exchange *storage.NativeExchange) (storageState, error) {
	var request, response struct {
		Store *bool `json:"store"`
	}
	if err := json.Unmarshal([]byte(exchange.RequestJSON), &request); err != nil {
		return storageState{}, fmt.Errorf("read native request storage: %w", err)
	}
	if err := json.Unmarshal([]byte(exchange.ResponseJSON), &response); err != nil {
		return storageState{}, fmt.Errorf("read native response storage: %w", err)
	}
	requested := request.Store == nil || *request.Store
	return storageState{Requested: requested, Available: requested && (response.Store == nil || *response.Store)}, nil
}
