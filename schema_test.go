package needle

import (
	"reflect"
	"testing"
)

func TestSchemaFor(t *testing.T) {
	t.Parallel()

	schema := SchemaFor[typedToolArguments]("weather", "Get the weather.")
	if schema.Name != "weather" || schema.Description != "Get the weather." {
		t.Fatalf("schema = %#v", schema)
	}
	if got := schema.Parameters["type"]; got != "object" {
		t.Fatalf("parameter type = %#v, want object", got)
	}
	for _, key := range []string{"$schema", "$id", "$ref", "$defs"} {
		if _, exists := schema.Parameters[key]; exists {
			t.Fatalf("parameters contain %q: %#v", key, schema.Parameters[key])
		}
	}
	properties, ok := schema.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", schema.Parameters["properties"])
	}
	city, ok := properties["city"].(map[string]any)
	if !ok || city["type"] != "string" || city["description"] != "City whose weather should be returned." {
		t.Fatalf("city schema = %#v", properties["city"])
	}
	units, ok := properties["units"].(map[string]any)
	if !ok || !reflect.DeepEqual(units["enum"], []any{"celsius", "fahrenheit"}) {
		t.Fatalf("units schema = %#v", properties["units"])
	}
	if got, want := schema.Parameters["required"], []any{"city"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %#v, want %#v", got, want)
	}
}
