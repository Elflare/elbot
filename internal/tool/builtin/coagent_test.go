package builtin

import (
	"encoding/json"
	"strconv"
	"testing"

	"elbot/internal/operationresult"
	"elbot/internal/tool/share"
)

func TestCoagentCreateSchemaAndBatchLimit(t *testing.T) {
	var create share.ToolDefinition
	for _, def := range coagentTools(nil) {
		if def.Definition.Name == "coagent_create" {
			create = def
		}
	}
	namesSchema := create.Parameters["properties"].(map[string]any)["names"].(map[string]any)
	if namesSchema["maxItems"] != 8 {
		t.Fatalf("names schema = %+v", namesSchema)
	}
	for _, count := range []int{8, 9} {
		var names []string
		for i := range count {
			names = append(names, strconv.Itoa(i))
		}
		args, err := json.Marshal(map[string]any{"goal": "测试", "names": names})
		if err != nil {
			t.Fatal(err)
		}
		result, err := create.PrepareCall(t.Context(), share.CallRequest{Arguments: args})
		if err != nil || (result.Status == operationresult.Success) != (count == 8) {
			t.Fatalf("%d names: %+v %v", count, result, err)
		}
	}
}
