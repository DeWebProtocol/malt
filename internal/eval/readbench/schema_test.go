package readbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type jsonSchema struct {
	Schema               string                  `json:"$schema"`
	ID                   string                  `json:"$id"`
	Title                string                  `json:"title"`
	Type                 string                  `json:"type"`
	AdditionalProperties bool                    `json:"additionalProperties"`
	Required             []string                `json:"required"`
	Properties           map[string]schemaObject `json:"properties"`
}

type schemaObject struct {
	Type string `json:"type"`
	Ref  string `json:"$ref"`
}

func TestResultSchemaIsCheckedInAndMatchesResultFields(t *testing.T) {
	schema := readResultSchema(t)
	if schema.Schema == "" || schema.ID == "" {
		t.Fatalf("schema must declare $schema and $id: %+v", schema)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatal("readbench result schema should reject unknown top-level fields")
	}

	wantFields, wantRequired := resultJSONFields(t)
	for fieldName := range wantFields {
		if _, ok := schema.Properties[fieldName]; !ok {
			t.Fatalf("schema missing Result field %q", fieldName)
		}
	}
	for fieldName := range schema.Properties {
		if _, ok := wantFields[fieldName]; !ok {
			t.Fatalf("schema has unknown top-level field %q", fieldName)
		}
	}
	for fieldName := range wantRequired {
		if !containsString(schema.Required, fieldName) {
			t.Fatalf("schema required list missing %q", fieldName)
		}
	}
}

func readResultSchema(t *testing.T) jsonSchema {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(ResultSchemaPath)))
	if err != nil {
		t.Fatalf("read schema %s: %v", ResultSchemaPath, err)
	}
	var schema jsonSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse schema %s: %v", ResultSchemaPath, err)
	}
	return schema
}

func resultJSONFields(t *testing.T) (map[string]struct{}, map[string]struct{}) {
	t.Helper()

	fields := make(map[string]struct{})
	required := make(map[string]struct{})
	resultType := reflect.TypeOf(Result{})
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		name, omitempty := jsonFieldName(field)
		if name == "" || name == "-" {
			continue
		}
		fields[name] = struct{}{}
		if !omitempty {
			required[name] = struct{}{}
		}
	}
	return fields, required
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name, false
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	if name == "" {
		name = field.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			return name, true
		}
	}
	return name, false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
