package tool

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var timeType = reflect.TypeOf(time.Time{})

// SchemaFromType derives a JSON Schema for t using reflection.
//
// Supported kinds: string, bool, all integer kinds, floats, slices/arrays,
// maps (loose "object"), and structs. Struct fields honor `json` tags for
// naming/omitempty and an optional `desc` tag for descriptions.
func SchemaFromType(t reflect.Type) (json.RawMessage, error) {
	s, err := schemaFor(t)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func schemaFor(t reflect.Type) (map[string]any, error) {
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaFor(t.Elem())
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaFor(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		return map[string]any{"type": "object"}, nil
	case reflect.Struct:
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, omitempty := parseJSONTag(f)
			if name == "-" {
				continue
			}
			fs, err := schemaFor(f.Type)
			if err != nil {
				return nil, fmt.Errorf("field %s.%s: %w", t.Name(), f.Name, err)
			}
			if d, ok := f.Tag.Lookup("desc"); ok {
				fs["description"] = d
			}
			props[name] = fs
			if !omitempty && f.Type.Kind() != reflect.Pointer {
				required = append(required, name)
			}
		}
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s, nil
	}
	return nil, fmt.Errorf("tool/schema: unsupported type %s", t)
}

// parseJSONTag extracts the field name and omitempty flag from a `json` tag.
func parseJSONTag(f reflect.StructField) (name string, omitempty bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok || tag == "" {
		return f.Name, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}
