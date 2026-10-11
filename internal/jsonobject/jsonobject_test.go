package jsonobject

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
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

func TestVisitArrayPreservesRawNestedValues(t *testing.T) {
	for _, raw := range []string{`[]`, `[1]`, `[null,true,"escaped\\\"value",{"nested":[1,{"x":2}]}]`} {
		var want []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		var got []json.RawMessage
		if err := VisitArray([]byte(raw), func(value []byte) error {
			got = append(got, json.RawMessage(value))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Fatalf("array=%s got=%s want=%s", raw, got, want)
		}
	}
	for _, raw := range []string{`null`, `[`, `[1,]`, `[{}] invalid`} {
		if err := VisitArray([]byte(raw), func([]byte) error { return nil }); err == nil {
			t.Fatalf("accepted invalid array: %s", raw)
		}
	}
	wantErr := errors.New("stop")
	if err := VisitArray([]byte(`[1,2]`), func([]byte) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("callback failure was lost: %v", err)
	}
}

func TestLookupUsesLastEscapedKeyWithoutDecodingContents(t *testing.T) {
	raw := []byte(`{"type":"message","content":{"nested":[1,2]},"\u0074ype":"function_call"}`)
	value, ok := Lookup(raw, "type")
	if !ok || string(value) != `"function_call"` {
		t.Fatalf("type=%s exists=%t", value, ok)
	}
	value, ok = Lookup(raw, "content")
	if !ok || string(value) != `{"nested":[1,2]}` {
		t.Fatalf("content=%s exists=%t", value, ok)
	}
	if _, ok = Lookup(raw, "absent"); ok {
		t.Fatal("missing key was found")
	}
}

func TestBytesWithValueRewritesExistingAndNewFields(t *testing.T) {
	for _, raw := range []string{`{"input":[1],"keep":true}`, `{"keep":true}`} {
		object, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		_ = object.Set("input", []byte(`[2]`))
		body, err := object.BytesWithValue("input", func(output *bytes.Buffer, value []byte) error {
			if string(value) != `[2]` {
				t.Fatalf("replacement did not receive the current value: %s", value)
			}
			output.WriteString(`[3]`)
			return nil
		})
		if err != nil || !bytes.Contains(body, []byte(`"input":[3]`)) || !json.Valid(body) {
			t.Fatalf("body=%s error=%v", body, err)
		}
		var appended bytes.Buffer
		object.AppendTo(&appended)
		if !bytes.Equal(appended.Bytes(), object.Bytes()) {
			t.Fatalf("append differs from standalone encoding: %s", appended.Bytes())
		}
		wantErr := errors.New("stop")
		if _, err := object.BytesWithValue("input", func(*bytes.Buffer, []byte) error { return wantErr }); !errors.Is(err, wantErr) {
			t.Fatalf("replacement failure was lost: %v", err)
		}
	}
}

func TestParseRejectsNonObject(t *testing.T) {
	for _, raw := range []string{"[]", "null", "{", `{"x":}`} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
