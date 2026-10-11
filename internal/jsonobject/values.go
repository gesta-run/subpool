package jsonobject

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Lookup returns the last matching field as a slice of validated JSON.
// Callers must validate raw first; nested values are never decoded or copied.
func Lookup(raw []byte, key string) ([]byte, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' {
		return nil, false
	}
	var value []byte
	for index := 1; ; {
		index = skipSpace(raw, index)
		if index >= len(raw) || raw[index] == '}' {
			return value, value != nil
		}
		end, ok := scanString(raw, index)
		if !ok {
			return nil, false
		}
		encodedKey := raw[index+1 : end-1]
		matches := string(encodedKey) == key
		if bytes.IndexByte(encodedKey, '\\') >= 0 {
			var decoded string
			if json.Unmarshal(raw[index:end], &decoded) != nil {
				return nil, false
			}
			matches = decoded == key
		}
		index = skipSpace(raw, end)
		if index >= len(raw) || raw[index] != ':' {
			return nil, false
		}
		start := skipSpace(raw, index+1)
		end = scanValueEnd(raw, start)
		if matches {
			value = bytes.TrimSpace(raw[start:end])
		}
		index = skipSpace(raw, end)
		if index >= len(raw) || raw[index] == '}' {
			return value, value != nil
		}
		index++
	}
}

// VisitArray visits validated array values without copying or decoding them.
func VisitArray(raw []byte, visit func([]byte) error) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '[' || !json.Valid(raw) {
		return errors.New("value must be a JSON array")
	}
	for index := 1; ; {
		index = skipSpace(raw, index)
		if raw[index] == ']' {
			return nil
		}
		end := scanValueEnd(raw, index)
		if err := visit(bytes.TrimSpace(raw[index:end])); err != nil {
			return err
		}
		index = skipSpace(raw, end)
		if raw[index] == ']' {
			return nil
		}
		index++
	}
}
