package responses

import (
	"strings"
	"testing"

	"elbot/internal/storage"
)

func TestContextLoadPreservesManifestOrderAndRejectsBrokenRecords(t *testing.T) {
	for _, name := range []string{"ordered", "missing parent", "cycle", "foreign checkpoint", "missing exchange", "missing input", "duplicate input", "extra input", "foreign input"} {
		t.Run(name, func(t *testing.T) {
			row := &storage.Session{ID: "s", Metadata: `{"llm_origin":{"protocol":"response","provider":"p"},"responses_input_version":2}`}
			checkpoint := &storage.NativeCheckpoint{ID: "cp", SessionID: row.ID, ExchangeID: "exchange", ResponseID: "response", CallsJSON: `[]`}
			exchange := storage.NativeExchange{ID: "exchange", SessionID: row.ID, APIType: "response", Provider: "p", Status: "completed", ResponseJSON: `{"id":"response","status":"completed"}`, ItemsJSON: `[]`, InputIDsJSON: `["second","first"]`}
			records := &storage.NativeWindowRecords{
				Checkpoints: map[string]storage.NativeCheckpoint{},
				Exchanges:   map[string]storage.NativeExchange{},
				Inputs: map[string]storage.NativeInput{
					"first":  {ID: "first", SessionID: row.ID, ConsumedBy: exchange.ID, ItemJSON: `{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}]}`, MediaJSON: `[]`},
					"second": {ID: "second", SessionID: row.ID, ConsumedBy: exchange.ID, ItemJSON: `{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}`, MediaJSON: `[]`},
				},
			}
			switch name {
			case "missing parent":
				checkpoint.ParentID = "absent"
			case "cycle":
				checkpoint.ParentID = checkpoint.ID
			case "foreign checkpoint":
				checkpoint.SessionID = "other"
			case "missing input":
				delete(records.Inputs, "first")
			case "duplicate input":
				exchange.InputIDsJSON = `["first","first"]`
			case "extra input":
				records.Inputs["extra"] = storage.NativeInput{ID: "extra", ConsumedBy: exchange.ID}
			case "foreign input":
				input := records.Inputs["first"]
				input.SessionID = "other"
				records.Inputs[input.ID] = input
			}
			records.Checkpoints[checkpoint.ID] = *checkpoint
			if name != "missing exchange" {
				records.Exchanges[exchange.ID] = exchange
			}
			window, err := (&Context{}).Load(t.Context(), row, checkpoint, records)
			if name != "ordered" {
				if err == nil {
					t.Fatalf("accepted broken records: %+v", window)
				}
				return
			}
			if err != nil || len(window.Items) != 2 || !strings.Contains(string(window.Items[0].Raw), "second") || !strings.Contains(string(window.Items[1].Raw), "first") {
				t.Fatalf("window=%+v error=%v", window, err)
			}
		})
	}
}
