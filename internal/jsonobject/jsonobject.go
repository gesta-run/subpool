package jsonobject

import (
	"bytes"
	"encoding/json"
	"errors"
)

type field struct {
	key   string
	value []byte
}

// Object keeps top-level values as slices of the original JSON. Nested values
// are not decoded, so large image data is copied only when Bytes builds the
// rewritten payload.
type Object struct {
	raw      []byte
	fields   []field
	last     map[string]int
	counts   map[string]int
	sets     map[string][]byte
	setOrder []string
	deleted  map[string]struct{}
}

func Parse(raw []byte) (*Object, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, errors.New("value must be a JSON object")
	}
	object := &Object{raw: trimmed, last: make(map[string]int), counts: make(map[string]int), sets: make(map[string][]byte), deleted: make(map[string]struct{})}
	for index := 1; ; {
		index = skipSpace(trimmed, index)
		if index >= len(trimmed) {
			return nil, errors.New("unterminated JSON object")
		}
		if trimmed[index] == '}' {
			if index != len(trimmed)-1 {
				return nil, errors.New("trailing JSON data")
			}
			return object, nil
		}
		keyStart := index
		keyEnd, ok := scanString(trimmed, keyStart)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		var key string
		if json.Unmarshal(trimmed[keyStart:keyEnd], &key) != nil {
			return nil, errors.New("invalid object key")
		}
		index = skipSpace(trimmed, keyEnd)
		if index >= len(trimmed) || trimmed[index] != ':' {
			return nil, errors.New("object key must be followed by a colon")
		}
		valueStart := skipSpace(trimmed, index+1)
		valueEnd := scanValueEnd(trimmed, valueStart)
		value := bytes.TrimSpace(trimmed[valueStart:valueEnd])
		if len(value) == 0 {
			return nil, errors.New("object value is empty")
		}
		object.fields = append(object.fields, field{key: key, value: value})
		object.last[key] = len(object.fields) - 1
		object.counts[key]++
		index = skipSpace(trimmed, valueEnd)
		if index >= len(trimmed) {
			return nil, errors.New("unterminated JSON object")
		}
		switch trimmed[index] {
		case ',':
			index++
		case '}':
			if index != len(trimmed)-1 {
				return nil, errors.New("trailing JSON data")
			}
			return object, nil
		default:
			return nil, errors.New("invalid JSON object delimiter")
		}
	}
}

func (o *Object) Duplicate(key string) bool {
	return o.counts[key] > 1
}

func (o *Object) Value(key string) ([]byte, bool) {
	if _, deleted := o.deleted[key]; deleted {
		return nil, false
	}
	if value, set := o.sets[key]; set {
		return value, true
	}
	index, found := o.last[key]
	if !found {
		return nil, false
	}
	return o.fields[index].value, true
}

func (o *Object) Set(key string, value []byte) error {
	if !json.Valid(value) {
		return errors.New("replacement value must be valid JSON")
	}
	if _, known := o.sets[key]; !known {
		o.setOrder = append(o.setOrder, key)
	}
	o.sets[key] = value
	delete(o.deleted, key)
	return nil
}

func (o *Object) Delete(key string) {
	delete(o.sets, key)
	o.deleted[key] = struct{}{}
}

func (o *Object) Bytes() []byte {
	output, _ := o.BytesWithValue("", nil)
	return output
}

// BytesWithValue streams one validated value into the final output buffer.
// The replacement must write valid JSON; nested values need not be copied first.
func (o *Object) BytesWithValue(key string, replace func(*bytes.Buffer, []byte) error) ([]byte, error) {
	var output bytes.Buffer
	output.Grow(len(o.raw) + 256)
	if err := o.writeTo(&output, key, replace); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// AppendTo writes into an existing buffer without allocating a nested payload.
func (o *Object) AppendTo(output *bytes.Buffer) {
	_ = o.writeTo(output, "", nil)
}

func (o *Object) writeTo(output *bytes.Buffer, key string, replace func(*bytes.Buffer, []byte) error) error {
	output.WriteByte('{')
	written := false
	write := func(name string, value []byte) error {
		if name == key && replace != nil {
			writeField(output, &written, name, nil)
			return replace(output, value)
		}
		writeField(output, &written, name, value)
		return nil
	}
	for index, field := range o.fields {
		if o.last[field.key] != index {
			continue
		}
		if _, deleted := o.deleted[field.key]; deleted {
			continue
		}
		value := field.value
		if replacement, set := o.sets[field.key]; set {
			value = replacement
		}
		if err := write(field.key, value); err != nil {
			return err
		}
	}
	for _, key := range o.setOrder {
		if _, existed := o.last[key]; existed {
			continue
		}
		if _, deleted := o.deleted[key]; deleted {
			continue
		}
		if err := write(key, o.sets[key]); err != nil {
			return err
		}
	}
	output.WriteByte('}')
	return nil
}

func writeField(output *bytes.Buffer, written *bool, key string, value []byte) {
	if *written {
		output.WriteByte(',')
	}
	encodedKey, _ := json.Marshal(key)
	output.Write(encodedKey)
	output.WriteByte(':')
	output.Write(value)
	*written = true
}

func skipSpace(raw []byte, index int) int {
	for index < len(raw) {
		switch raw[index] {
		case ' ', '\t', '\n', '\r':
			index++
		default:
			return index
		}
	}
	return index
}

func scanString(raw []byte, start int) (int, bool) {
	if start >= len(raw) || raw[start] != '"' {
		return start, false
	}
	escaped := false
	for index := start + 1; index < len(raw); index++ {
		if escaped {
			escaped = false
			continue
		}
		switch raw[index] {
		case '\\':
			escaped = true
		case '"':
			return index + 1, true
		}
	}
	return start, false
}

func scanValueEnd(raw []byte, start int) int {
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(raw); index++ {
		value := raw[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return index
			}
			depth--
		case ',':
			if depth == 0 {
				return index
			}
		}
	}
	return len(raw)
}
