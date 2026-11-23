package builtin

import (
	"encoding/json"
	"slices"
	"strconv"
	"testing"

	"elbot/internal/contextinfo"
	"elbot/internal/operationresult"
	"elbot/internal/security"
	"elbot/internal/tool/discovery"
	"elbot/internal/tool/registry"
	"elbot/internal/tool/share"
)

func TestCoagentDiscoveryActivatesOnlyMemberOperations(t *testing.T) {
	r := registry.NewRegistry(security.DefaultPermission())
	if _, err := r.RegisterTools(t.Context(), share.SourceBuiltin, share.ToolRegistrationScope{}, coagentTools(nil)); err != nil {
		t.Fatal(err)
	}
	resolver := discovery.NewResolver(r)
	for _, tc := range []struct {
		task string
		want []string
	}{
		{"", []string{"coagent_create", "coagent_post", "coagent_pin", "coagent_board", "coagent_remove"}},
		{"coagent", []string{"coagent_post", "coagent_board"}},
	} {
		ctx := contextinfo.WithActor(t.Context(), contextinfo.Actor{Role: contextinfo.RoleUser})
		ctx = contextinfo.WithExecution(ctx, contextinfo.Execution{Mode: "work", TaskType: tc.task})
		ctx = withBuiltinToolAvailability(t, ctx, r)
		list, err := resolver.Discover(ctx, nil)
		if err != nil || len(list.Tools) != 1 || list.Tools[0].Info.Name != "coagent" {
			t.Fatalf("task %q: summary=%+v err=%v", tc.task, list, err)
		}
		res, _, err := callTool(ctx, discovery.NewDiscoverTool(resolver), share.CallRequest{Arguments: json.RawMessage(`{"name":"coagent"}`)})
		if err != nil || res.Status != operationresult.Success {
			t.Fatal(res, err)
		}
		update, err := resolver.DiscoveryStateUpdate(ctx, res.Data)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, item := range update.Tools {
			names = append(names, item.Schema.Name)
		}
		if !slices.Equal(names, tc.want) {
			t.Fatalf("task %q: schemas=%v, want %v", tc.task, names, tc.want)
		}
	}
}
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
	for _, args := range []string{`{}`, `{"goal":" ","names":["a"]}`, `{"goal":"x","names":[]}`, `{"goal":"x","names":["a"," a "]}`} {
		res, err := create.PrepareCall(t.Context(), share.CallRequest{Arguments: json.RawMessage(args)})
		if err != nil || res.Status != operationresult.InvalidArguments || res.Data != nil {
			t.Fatalf("%s passed preparation: %+v %v", args, res, err)
		}
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
