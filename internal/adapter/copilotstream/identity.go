package copilotstream

import "encoding/json"

// Identity keeps one native upstream item ID per output slot for a single response.
// Copilot can return different opaque IDs for the same slot in successive events.
// Reuse the first upstream ID instead of inventing one that history cannot replay.
type Identity struct {
	items map[int64]string
}

// Normalize runs before tool restoration so argument deltas and completed calls
// share the same identity too. Response IDs, call IDs and encrypted content remain
// upstream-owned; in particular, the terminal response ID is used for continuation.
func (s *Identity) Normalize(event map[string]any) {
	if index, ok := outputIndex(event["output_index"]); ok {
		if item, ok := event["item"].(map[string]any); ok {
			s.normalizeItem(index, item)
		}
		if id, ok := event["item_id"].(string); ok && id != "" {
			event["item_id"] = s.itemID(index, id)
		}
	}
	if response, ok := event["response"].(map[string]any); ok {
		output, _ := response["output"].([]any)
		for index, entry := range output {
			if item, ok := entry.(map[string]any); ok {
				s.normalizeItem(int64(index), item)
			}
		}
	}
}

func (s *Identity) normalizeItem(index int64, item map[string]any) {
	if id, ok := item["id"].(string); ok && id != "" {
		item["id"] = s.itemID(index, id)
	}
}

func (s *Identity) itemID(index int64, upstream string) string {
	if id := s.items[index]; id != "" {
		return id
	}
	if s.items == nil {
		s.items = make(map[int64]string)
	}
	s.items[index] = upstream
	return upstream
}

func outputIndex(value any) (int64, bool) {
	var index int64
	switch value := value.(type) {
	case json.Number:
		var err error
		index, err = value.Int64()
		if err != nil {
			return 0, false
		}
	case float64:
		if value < 0 || value >= 1<<63 {
			return 0, false
		}
		index = int64(value)
		if float64(index) != value {
			return 0, false
		}
	case int:
		index = int64(value)
	default:
		return 0, false
	}
	return index, index >= 0
}
