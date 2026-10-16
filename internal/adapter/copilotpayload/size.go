package copilotpayload

import (
	"bytes"
	"encoding/json"

	"github.com/gesta-run/subpool/internal/jsonobject"
)

// Sizes contains serialized UTF-8 byte counts, never customer content. Images
// are a subset of message/tool-output bytes, not an additional disjoint bucket.
type Sizes struct {
	Instructions int
	Tools        int
	Messages     int
	ToolOutputs  int
	Images       int
}

func Measure(raw []byte) Sizes {
	var sizes Sizes
	object, err := jsonobject.Parse(raw)
	if err != nil {
		return sizes
	}
	instructions, _ := object.Value("instructions")
	sizes.Instructions = len(instructions)
	tools, _ := object.Value("tools")
	sizes.Tools = len(tools)
	for _, key := range []string{"input", "messages"} {
		items, _ := object.Value(key)
		if items = bytes.TrimSpace(items); len(items) == 0 {
			continue
		}
		if items[0] != '[' {
			sizes.Messages += len(items)
			continue
		}
		_ = jsonobject.VisitArray(items, func(item []byte) error {
			kind, _ := jsonobject.Lookup(item, "type")
			role, _ := jsonobject.Lookup(item, "role")
			switch {
			case bytes.Equal(kind, []byte(`"additional_tools"`)):
				sizes.Tools += len(item)
			case bytes.Equal(role, []byte(`"system"`)), bytes.Equal(role, []byte(`"developer"`)):
				sizes.Instructions += len(item)
			case bytes.Equal(role, []byte(`"tool"`)), bytes.Equal(kind, []byte(`"function_call_output"`)), bytes.Equal(kind, []byte(`"custom_tool_call_output"`)):
				sizes.ToolOutputs += len(item)
			default:
				sizes.Messages += len(item)
			}
			sizes.Images += imageBytes(item)
			return nil
		})
	}
	return sizes
}

func imageBytes(raw []byte) int {
	// Only visit known content containers. Do not interpret JSON embedded in
	// ordinary text or recurse through arbitrary tool schemas.
	for _, key := range []string{"content", "output"} {
		content, _ := jsonobject.Lookup(raw, key)
		if content = bytes.TrimSpace(content); len(content) == 0 || content[0] != '[' {
			continue
		}
		total := 0
		_ = jsonobject.VisitArray(content, func(part []byte) error {
			kind, _ := jsonobject.Lookup(part, "type")
			var value string
			_ = json.Unmarshal(kind, &value)
			if value == "input_image" || value == "image_url" {
				total += len(part)
			}
			return nil
		})
		if total > 0 {
			return total
		}
	}
	return 0
}
