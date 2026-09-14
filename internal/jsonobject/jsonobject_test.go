package jsonobject

import (
	"encoding/json"
	"testing"
)

func TestRewritePreservesNestedValuesAndUsesLastDuplicate(t *testing.T) {
	object, err := Parse([]byte(` {"input":[{"content":"large"}],"remove":1,"keep":true,"keep":false} `))
	if err != nil {
		t.Fatal(err)
	}
	if !object.Duplicate("keep") || object.Duplicate("input") {
		t.Fatal("duplicate field tracking is incorrect")
	}
	object.Delete("remove")
	if err = object.Set("stream", []byte("true")); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(object.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["keep"] != false || value["stream"] != true || value["remove"] != nil {
		t.Fatalf("rewritten object = %#v", value)
	}
	input := value["input"].([]any)
	if input[0].(map[string]any)["content"] != "large" {
		t.Fatalf("input changed: %#v", input)
	}
}

func TestParseRejectsNonObject(t *testing.T) {
	for _, raw := range []string{"[]", "null", "{", `{"x":}`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
