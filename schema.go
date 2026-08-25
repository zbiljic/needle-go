package needle

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"
)

// SchemaFor builds a tool schema whose parameters are derived from T. JSON
// field names and optionality follow encoding/json tags; JSON Schema
// annotations can be supplied with jsonschema tags.
func SchemaFor[T any](name, description string) ToolSchema {
	return ToolSchema{
		Name:        name,
		Description: description,
		Parameters:  parametersFor[T](),
	}
}

func parametersFor[T any]() map[string]any {
	reflector := jsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
		ExpandedStruct: true,
	}
	reflected := reflector.ReflectFromType(reflect.TypeFor[T]())
	reflected.Version = ""

	data, err := json.Marshal(reflected)
	if err != nil {
		panic(fmt.Errorf("needle: encode schema for %s: %w", reflect.TypeFor[T](), err))
	}
	var parameters map[string]any
	if err := json.Unmarshal(data, &parameters); err != nil {
		panic(fmt.Errorf("needle: decode schema for %s: %w", reflect.TypeFor[T](), err))
	}
	return parameters
}
