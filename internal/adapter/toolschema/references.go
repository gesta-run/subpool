package toolschema

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const (
	maxExpansionDepth   = 64
	maxExpandedBranches = 256
	maxExpansionWeight  = 1 << 20
)

type referenceResolver struct {
	root      map[string]any
	branches  int
	remaining int
	nested    bool
}

func (r *referenceResolver) resolve(value any, seen []string) (map[string]any, error) {
	schema, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("oneOf branches must be schema objects")
	}
	if _, scoped := schema["$id"]; scoped {
		return nil, fmt.Errorf("cannot expand references across nested schema identifiers")
	}
	encoded, exists := schema["$ref"]
	if !exists {
		return schema, nil
	}
	ref, ok := encoded.(string)
	if !ok || slices.Contains(seen, ref) || len(seen) >= maxExpansionDepth {
		return nil, fmt.Errorf("invalid, recursive, or excessively deep oneOf reference")
	}
	target, err := r.lookup(ref)
	if err != nil {
		return nil, err
	}
	resolved, err := r.resolve(target, append(seen, ref))
	if err != nil {
		return nil, err
	}
	result := maps.Clone(resolved)
	for key, sibling := range schema {
		if key == "$ref" {
			continue
		}
		if !annotation(key) {
			// Only type and required are independent of sibling keyword scope.
			// Moving properties or additionalProperties across a reference can
			// change validation even when duplicate keyword values are identical.
			if key != "type" && key != "required" {
				return nil, fmt.Errorf("cannot expand oneOf reference with sibling %q validation constraint", key)
			}
			if existing, present := result[key]; present && !reflect.DeepEqual(existing, sibling) {
				return nil, fmt.Errorf("cannot merge oneOf reference with conflicting %q constraint", key)
			}
		}
		result[key] = sibling
	}
	return result, nil
}

func (r *referenceResolver) lookup(ref string) (any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("oneOf references must be local JSON pointers")
	}
	pointer, err := url.PathUnescape(ref[2:])
	if err != nil {
		return nil, fmt.Errorf("invalid oneOf JSON pointer")
	}
	var value any = r.root
	for step, segment := range strings.Split(pointer, "/") {
		escapesRemoved := strings.ReplaceAll(strings.ReplaceAll(segment, "~0", ""), "~1", "")
		if strings.Contains(escapesRemoved, "~") {
			return nil, fmt.Errorf("invalid oneOf JSON pointer escape")
		}
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		switch object := value.(type) {
		case map[string]any:
			if _, nestedID := object["$id"].(string); nestedID && step > 0 {
				return nil, fmt.Errorf("cannot resolve oneOf reference through a nested schema identifier")
			}
			var exists bool
			value, exists = object[segment]
			if !exists {
				return nil, fmt.Errorf("oneOf reference target does not exist")
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(object) || strconv.Itoa(index) != segment {
				return nil, fmt.Errorf("invalid oneOf reference array index")
			}
			value = object[index]
		default:
			return nil, fmt.Errorf("oneOf reference target does not exist")
		}
	}
	return value, nil
}

func (r *referenceResolver) inheritAnnotations(parent, child map[string]any) (map[string]any, error) {
	result := maps.Clone(child)
	for key, value := range parent {
		if !annotation(key) {
			continue
		}
		existing, present := result[key]
		if present && !reflect.DeepEqual(existing, value) {
			return nil, fmt.Errorf("cannot flatten oneOf with conflicting %q annotations", key)
		}
		if !present {
			if err := r.charge(value, 0); err != nil {
				return nil, err
			}
			result[key] = value
		}
	}
	return result, nil
}

func annotation(key string) bool {
	switch key {
	case "title", "description", "$comment", "default", "examples", "deprecated", "readOnly", "writeOnly":
		return true
	default:
		return false
	}
}

// Count expansion weight before serialization, including literal JSON values.
// The budget bounds repeated large definitions as well as the branch count.
func (r *referenceResolver) charge(value any, depth int) error {
	if depth > maxExpansionDepth {
		return fmt.Errorf("oneOf expansion exceeds depth limit")
	}
	r.remaining--
	switch object := value.(type) {
	case map[string]any:
		for key, child := range object {
			r.remaining -= len(key)
			if err := r.charge(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range object {
			if err := r.charge(child, depth+1); err != nil {
				return err
			}
		}
	case string:
		r.remaining -= len(object)
	case json.Number:
		r.remaining -= len(object)
	}
	if r.remaining < 0 {
		return fmt.Errorf("oneOf expansion exceeds size limit")
	}
	return nil
}
