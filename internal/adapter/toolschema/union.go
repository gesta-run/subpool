// Package toolschema adapts tool parameter schemas without weakening validation.
package toolschema

import (
	"fmt"
	"maps"
)

// FlattenRootObjectUnion resolves references in root oneOf branches and flattens
// pure nested object unions. Grok rejects those shapes in function parameters.
// Property schemas, definitions, and root constraints remain unchanged.
func FlattenRootObjectUnion(value any) (any, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	branches, ok := root["oneOf"].([]any)
	if !ok || !needsFlattening(branches) {
		return value, nil
	}
	resolver := referenceResolver{root: root, remaining: maxExpansionWeight}
	flat, err := resolver.flatten(branches, 0)
	if err != nil {
		return nil, err
	}
	// Exclusive unions are not associative when alternatives overlap. Only
	// flatten branches separated by required string enum/const discriminators.
	for index, branch := range flat {
		if !resolver.nested {
			break
		}
		for _, other := range flat[:index] {
			if !resolver.disjoint(branch.(map[string]any), other.(map[string]any)) {
				return nil, fmt.Errorf("cannot safely flatten overlapping oneOf branches")
			}
		}
	}
	result := maps.Clone(root)
	result["oneOf"] = flat
	return result, nil
}

func needsFlattening(branches []any) bool {
	for _, branch := range branches {
		object, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		if _, exists := object["$ref"]; exists {
			return true
		}
		if _, exists := object["oneOf"]; exists {
			return true
		}
	}
	return false
}

func (r *referenceResolver) flatten(branches []any, depth int) ([]any, error) {
	if depth > maxExpansionDepth || len(branches) == 0 {
		return nil, fmt.Errorf("oneOf expansion exceeds depth limit or has no branches")
	}
	var flat []any
	for _, value := range branches {
		branch, err := r.resolve(value, nil)
		if err != nil {
			return nil, err
		}
		if nested, exists := branch["oneOf"]; exists {
			r.nested = true
			children, ok := nested.([]any)
			if !ok || !pureObjectUnion(branch) {
				return nil, fmt.Errorf("cannot flatten oneOf branch with additional validation constraints")
			}
			children, err = r.flatten(children, depth+1)
			if err != nil {
				return nil, err
			}
			for index, child := range children {
				children[index], err = r.inheritAnnotations(branch, child.(map[string]any))
				if err != nil {
					return nil, err
				}
			}
			flat = append(flat, children...)
			continue
		}
		if branch["type"] != "object" {
			return nil, fmt.Errorf("oneOf expansion requires object branches")
		}
		if err := r.charge(branch, 0); err != nil {
			return nil, err
		}
		r.branches++
		if r.branches > maxExpandedBranches {
			return nil, fmt.Errorf("oneOf expansion exceeds branch limit")
		}
		flat = append(flat, branch)
	}
	return flat, nil
}

func pureObjectUnion(branch map[string]any) bool {
	for key, value := range branch {
		switch key {
		case "oneOf":
		case "type":
			if value != "object" {
				return false
			}
		case "properties":
			if properties, ok := value.(map[string]any); !ok || len(properties) != 0 {
				return false
			}
		default:
			if !annotation(key) {
				return false
			}
		}
	}
	return true
}

func (r *referenceResolver) disjoint(left, right map[string]any) bool {
	leftProperties, _ := left["properties"].(map[string]any)
	rightProperties, _ := right["properties"].(map[string]any)
	for name, property := range leftProperties {
		if !requires(left, name) || !requires(right, name) {
			continue
		}
		leftValues := r.stringValues(property)
		rightValues := r.stringValues(rightProperties[name])
		if len(leftValues) == 0 || len(rightValues) == 0 {
			continue
		}
		if separated(leftValues, rightValues) {
			return true
		}
	}
	return false
}

func separated(left, right []string) bool {
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, shared := values[value]; shared {
			return false
		}
	}
	return true
}

func requires(schema map[string]any, name string) bool {
	required, _ := schema["required"].([]any)
	for _, value := range required {
		if value == name {
			return true
		}
	}
	return false
}

func (r *referenceResolver) stringValues(value any) []string {
	schema, err := r.resolve(value, nil)
	if err != nil {
		return nil
	}
	if constant, ok := schema["const"].(string); ok {
		return []string{constant}
	}
	enum, _ := schema["enum"].([]any)
	values := make([]string, 0, len(enum))
	for _, entry := range enum {
		value, ok := entry.(string)
		if !ok {
			return nil
		}
		values = append(values, value)
	}
	return values
}
