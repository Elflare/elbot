package toolrun

import (
	"reflect"

	"elbot/internal/llm"
)

// Schemas are JSON trees. Preserve scalar Go types while detaching containers.
func cloneSchema(schema llm.ToolSchema) llm.ToolSchema {
	if schema.Function.Parameters != nil {
		schema.Function.Parameters = cloneSchemaValue(reflect.ValueOf(schema.Function.Parameters)).Interface().(map[string]any)
	}
	return schema
}

func cloneSchemaValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneSchemaValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneSchemaValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneSchemaValue(v.Index(i)))
		}
		return out
	default:
		return v
	}
}
