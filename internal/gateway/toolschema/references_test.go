package toolschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReferenceExpansionRejectsScopedSiblingConstraints(t *testing.T) {
	for _, test := range []struct{ name, source, keyword string }{
		{
			"sibling closes referenced properties",
			`{"oneOf":[{"$ref":"#/$defs/base","additionalProperties":false}],"$defs":{"base":{"type":"object","properties":{"id":{"type":"string"}}}}}`,
			"additionalProperties",
		},
		{
			"reference closes sibling properties",
			`{"oneOf":[{"$ref":"#/$defs/base","properties":{"id":{"type":"string"}}}],"$defs":{"base":{"type":"object","additionalProperties":false}}}`,
			"properties",
		},
		{
			"identical additionalProperties still has different scope",
			`{"oneOf":[{"$ref":"#/$defs/base","additionalProperties":false}],"$defs":{"base":{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}}}`,
			"additionalProperties",
		},
		{
			"reference chain has scoped siblings",
			`{"oneOf":[{"$ref":"#/$defs/alias"}],"$defs":{"alias":{"$ref":"#/$defs/base","additionalProperties":false},"base":{"type":"object","properties":{"id":{"type":"string"}}}}}`,
			"additionalProperties",
		},
		{
			"unevaluated properties rely on evaluation scope",
			`{"oneOf":[{"$ref":"#/$defs/base","unevaluatedProperties":false}],"$defs":{"base":{"type":"object"}}}`,
			"unevaluatedProperties",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := decodeSchema(t, test.source)
			before, _ := json.Marshal(schema)
			result, err := FlattenRootObjectUnion(schema)
			// Assert rejection directly, not through the converter's resolver:
			// no weakened schema may be returned for upstream use.
			if result != nil || err == nil || !strings.Contains(err.Error(), "sibling \""+test.keyword+"\" validation constraint") {
				t.Fatalf("unsafe reference expansion was not rejected: result=%#v error=%v", result, err)
			}
			after, _ := json.Marshal(schema)
			if string(before) != string(after) {
				t.Fatal("rejected expansion mutated the original schema")
			}
		})
	}
}

func TestReferenceExpansionPreservesIndependentSiblings(t *testing.T) {
	schema := decodeSchema(t, `{"oneOf":[{"$ref":"#/$defs/base","type":"object","required":["id"],"description":"Illustrative branch"}],"$defs":{"base":{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}}}`)
	result, err := FlattenRootObjectUnion(schema)
	if err != nil {
		t.Fatal(err)
	}
	branch := result.(map[string]any)["oneOf"].([]any)[0].(map[string]any)
	if branch["type"] != "object" || !requires(branch, "id") || branch["description"] != "Illustrative branch" || branch["additionalProperties"] != false {
		t.Fatalf("independent siblings were changed: %#v", branch)
	}
}
