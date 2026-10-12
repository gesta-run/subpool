package toolschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// An illustrative scheduler schema with no customer data or captured requests.
const schedulerSchema = `{
  "type":"object", "properties":{}, "description":"Illustrative scheduler",
  "oneOf":[{"$ref":"#/$defs/view"},{"$ref":"#/$defs/create"},{"$ref":"#/$defs/update"},{"$ref":"#/$defs/delete"}],
  "$defs":{
    "mode":{"type":"string"},
    "view":{"type":"object","properties":{"mode":{"enum":["view"]},"id":{"$ref":"#/$defs/mode"}},"required":["mode","id"],"additionalProperties":false},
    "create":{"oneOf":[{"$ref":"#/$defs/createCron"},{"$ref":"#/$defs/createHeartbeat"}]},
    "createCron":{"type":"object","properties":{"mode":{"enum":["create"]},"kind":{"enum":["cron"]}},"required":["mode","kind"],"additionalProperties":false},
    "createHeartbeat":{"type":"object","properties":{"mode":{"enum":["create"]},"kind":{"enum":["heartbeat"]}},"required":["mode","kind"],"additionalProperties":false},
    "update":{"oneOf":[{"$ref":"#/$defs/updateCron"},{"$ref":"#/$defs/updateHeartbeat"}]},
    "updateCron":{"type":"object","properties":{"mode":{"enum":["update"]},"kind":{"enum":["cron"]},"id":{"type":"string"}},"required":["mode","kind","id"],"additionalProperties":false},
    "updateHeartbeat":{"type":"object","properties":{"mode":{"enum":["update"]},"kind":{"enum":["heartbeat"]},"id":{"type":"string"}},"required":["mode","kind","id"],"additionalProperties":false},
    "delete":{"type":"object","properties":{"mode":{"enum":["delete"]},"id":{"type":"string"}},"required":["mode","id"],"additionalProperties":false}
  }
}`

func decodeSchema(t *testing.T, source string) map[string]any {
	t.Helper()
	var schema map[string]any
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestFlattenRootObjectUnionPreservesConstraints(t *testing.T) {
	original := decodeSchema(t, schedulerSchema)
	before, _ := json.Marshal(original)
	value, err := FlattenRootObjectUnion(original)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(map[string]any)
	branches := result["oneOf"].([]any)
	if len(branches) != 6 || !reflect.DeepEqual(result["$defs"], original["$defs"]) || result["description"] != original["description"] {
		t.Fatalf("schema changed unexpectedly: %#v", result)
	}
	for _, entry := range branches {
		branch := entry.(map[string]any)
		if branch["type"] != "object" || branch["$ref"] != nil || branch["oneOf"] != nil || branch["additionalProperties"] != false {
			t.Fatalf("invalid flattened branch: %#v", branch)
		}
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("original schema was mutated")
	}
	for _, mode := range []string{"view", "create", "update", "delete", "unknown"} {
		for _, kind := range []string{"", "cron", "heartbeat", "unknown"} {
			for _, includeID := range []bool{false, true} {
				candidate := map[string]any{"mode": mode}
				if kind != "" {
					candidate["kind"] = kind
				}
				if includeID {
					candidate["id"] = "illustrative-id"
				}
				if accepts(original, original, candidate) != accepts(original, result, candidate) {
					t.Fatalf("validation changed for %#v", candidate)
				}
			}
		}
	}
}

// Evaluate only the constraints used by the illustrative fixtures.
func accepts(root, schema map[string]any, value any) bool {
	resolver := referenceResolver{root: root}
	resolved, err := resolver.resolve(schema, nil)
	if err != nil {
		return false
	}
	if choices, ok := resolved["oneOf"].([]any); ok {
		matches := 0
		for _, choice := range choices {
			if accepts(root, choice.(map[string]any), value) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	if enum, ok := resolved["enum"].([]any); ok {
		found := false
		for _, candidate := range enum {
			found = found || reflect.DeepEqual(candidate, value)
		}
		if !found {
			return false
		}
	}
	if resolved["type"] == "string" {
		_, ok := value.(string)
		return ok
	}
	object, ok := value.(map[string]any)
	if resolved["type"] == "object" && !ok {
		return false
	}
	required, _ := resolved["required"].([]any)
	for _, name := range required {
		if _, exists := object[name.(string)]; !exists {
			return false
		}
	}
	properties, _ := resolved["properties"].(map[string]any)
	for name, child := range object {
		property, exists := properties[name]
		if !exists && resolved["additionalProperties"] == false {
			return false
		}
		if exists && !accepts(root, property.(map[string]any), child) {
			return false
		}
	}
	return true
}

func TestFlattenRootObjectUnionRejectsUnsafeExpansion(t *testing.T) {
	for _, test := range []struct{ name, source, message string }{
		{"overlap", `{"oneOf":[{"oneOf":[{"type":"object","properties":{"mode":{"enum":["view"]}},"required":["mode"]},{"type":"object","properties":{"mode":{"enum":["view"]}},"required":["mode"]}]}]}`, "overlapping"},
		{"optional discriminator", `{"oneOf":[{"oneOf":[{"type":"object","properties":{"mode":{"enum":["view"]}}},{"type":"object","properties":{"mode":{"enum":["delete"]}}}]}]}`, "overlapping"},
		{"numeric discriminator", `{"oneOf":[{"oneOf":[{"type":"object","properties":{"mode":{"enum":[1]}},"required":["mode"]},{"type":"object","properties":{"mode":{"enum":[1.0]}},"required":["mode"]}]}]}`, "overlapping"},
		{"nested constraints", `{"oneOf":[{"required":["id"],"oneOf":[{"type":"object"}]}]}`, "additional validation"},
		{"missing reference", `{"oneOf":[{"$ref":"#/$defs/missing"}]}`, "does not exist"},
		{"remote reference", `{"oneOf":[{"$ref":"https://example.invalid/schema"}]}`, "local JSON pointers"},
		{"recursive reference", `{"oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}}}`, "recursive"},
		{"recursive union", `{"oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"oneOf":[{"$ref":"#/$defs/a"}]}}}`, "depth limit"},
		{"conflicting sibling", `{"oneOf":[{"$ref":"#/$defs/a","type":"string"}],"$defs":{"a":{"type":"object"}}}`, "conflicting"},
		{"nested identifier", `{"oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"$id":"https://example.invalid/schema","type":"object"}}}`, "schema identifiers"},
		{"non-object branch", `{"oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"type":"string"}}}`, "object branches"},
		{"invalid pointer escape", `{"oneOf":[{"$ref":"#/$defs/a~2b"}],"$defs":{"a~2b":{"type":"object"}}}`, "pointer escape"},
		{"conflicting annotation", `{"oneOf":[{"title":"Parent","oneOf":[{"type":"object","title":"Child"}]}]}`, "conflicting"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := FlattenRootObjectUnion(decodeSchema(t, test.source))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error=%v, expected %q", err, test.message)
			}
		})
	}
}

func TestFlattenRootObjectUnionPreservesUnrelatedSchemasAndLiterals(t *testing.T) {
	for _, source := range []string{
		`{"type":"object","properties":{"value":{"$ref":"#/$defs/value"}},"$defs":{"value":{"type":"string"}}}`,
		`{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"number"}]}}}`,
		`{"oneOf":[{"type":"object"},{"type":"object"}]}`,
		`{"anyOf":[{"oneOf":[{"type":"object"}]}]}`,
	} {
		original := decodeSchema(t, source)
		result, err := FlattenRootObjectUnion(original)
		if err != nil || !reflect.DeepEqual(original, result) {
			t.Fatalf("unrelated schema was changed: %#v error=%v", result, err)
		}
	}
	schema := decodeSchema(t, `{"oneOf":[{"$ref":"#/$defs/a"}],"$defs":{"a":{"type":"object","properties":{"oneOf":{"default":{"$ref":"literal","oneOf":[1,2]},"const":"illustrative"}},"example":9007199254740993}}}`)
	result, err := FlattenRootObjectUnion(schema)
	if err != nil {
		t.Fatal(err)
	}
	branch := result.(map[string]any)["oneOf"].([]any)[0]
	if !reflect.DeepEqual(branch, schema["$defs"].(map[string]any)["a"]) {
		t.Fatal("literal JSON, numeric precision, or property schemas were modified")
	}
}

func TestFlattenRootObjectUnionReferenceDetails(t *testing.T) {
	schema := decodeSchema(t, `{"$id":"https://example.invalid/root","oneOf":[{"$ref":"#/$defs/a~1b~0c","description":"Branch annotation","required":["id"]}],"$defs":{"a/b~c":{"type":"object","properties":{"id":{"type":"string"}}}}}`)
	result, err := FlattenRootObjectUnion(schema)
	if err != nil {
		t.Fatal(err)
	}
	branch := result.(map[string]any)["oneOf"].([]any)[0].(map[string]any)
	if branch["description"] != "Branch annotation" || !requires(branch, "id") {
		t.Fatal("reference sibling constraints or annotations were lost")
	}
	schema = decodeSchema(t, `{"oneOf":[{"$ref":"#/$defs/union"}],"$defs":{"$id":{"type":"object"},"union":{"description":"Inherited annotation","oneOf":[{"type":"object"}]}}}`)
	result, err = FlattenRootObjectUnion(schema)
	if err != nil || result.(map[string]any)["oneOf"].([]any)[0].(map[string]any)["description"] != "Inherited annotation" {
		t.Fatalf("nested annotation or definition named $id was mishandled: result=%#v error=%v", result, err)
	}
}

func TestFlattenRootObjectUnionReferencedDiscriminators(t *testing.T) {
	schema := decodeSchema(t, `{"oneOf":[{"oneOf":[
		{"type":"object","properties":{"mode":{"$ref":"#/$defs/view"}},"required":["mode"]},
		{"type":"object","properties":{"mode":{"$ref":"#/$defs/remove"}},"required":["mode"]}
	]}, {"type":"object","properties":{"mode":{"const":"create"}},"required":["mode"]}],
	"$defs":{"view":{"$ref":"#/$defs/viewValue"},"viewValue":{"const":"view"},"remove":{"enum":["delete"]}}}`)
	result, err := FlattenRootObjectUnion(schema)
	if err != nil {
		t.Fatal(err)
	}
	branches := result.(map[string]any)["oneOf"].([]any)
	if len(branches) != 3 || branches[0].(map[string]any)["properties"].(map[string]any)["mode"].(map[string]any)["$ref"] != "#/$defs/view" {
		t.Fatal("referenced enum/const discriminators were not preserved")
	}
}

func TestFlattenRootObjectUnionExpansionLimits(t *testing.T) {
	for _, limit := range []string{"branch", "string size", "number size"} {
		t.Run(limit, func(t *testing.T) {
			schema := decodeSchema(t, `{"oneOf":[{"$ref":"#/$defs/leaf"}],"$defs":{"leaf":{"type":"object"}}}`)
			if limit == "branch" {
				branches := make([]any, maxExpandedBranches+1)
				for index := range branches {
					branches[index] = map[string]any{"$ref": "#/$defs/leaf"}
				}
				schema["oneOf"] = branches
			} else if limit == "string size" {
				schema["$defs"].(map[string]any)["leaf"].(map[string]any)["description"] = strings.Repeat("x", maxExpansionWeight)
			} else {
				schema["$defs"].(map[string]any)["leaf"].(map[string]any)["default"] = json.Number(strings.Repeat("9", maxExpansionWeight))
			}
			_, err := FlattenRootObjectUnion(schema)
			kind := "size"
			if limit == "branch" {
				kind = "branch"
			}
			if err == nil || !strings.Contains(err.Error(), kind+" limit") {
				t.Fatalf("expansion limit not enforced: %v", err)
			}
		})
	}
}
