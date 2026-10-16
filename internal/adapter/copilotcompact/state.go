package copilotcompact

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const statePrefix = "subpool-copilot-v1."

// Opaque state is bounded independently of request admission. It is not an
// OpenAI/Copilot encrypted item, and must always be restored before inference.
const MaxStateBytes = 1 << 20

var ErrInvalidState = errors.New("invalid or foreign Subpool compaction state; replay the full conversation or compact again")

type ScopedCipher interface {
	Seal(plaintext, associatedData []byte) ([]byte, error)
	Open(ciphertext, associatedData []byte) ([]byte, error)
}

type State struct {
	Cipher ScopedCipher
	Scope  string
}

type savedWindow struct {
	Window  []json.RawMessage `json:"window"`
	Covered []string          `json:"covered"`
}

func (state *State) Seal(window []json.RawMessage, covered []string, triggerID string) (json.RawMessage, error) {
	item, err := newItem(triggerID)
	if err != nil {
		return nil, err
	}
	return state.sealItem(window, covered, item)
}

// ValidateStateSize serializes the same envelope as sealing. A JSON string can
// expand to six bytes per input byte, including control characters and HTML.
// An unchanged window needs no summary allowance.
func ValidateStateSize(plan Plan, covered []string) error {
	summary := ""
	if len(plan.Parts) > 0 {
		summary = strings.Repeat("\x00", MaxSummaryBytes)
	}
	_, err := encodeWindow(Window(plan, summary), covered)
	return err
}

func encodeWindow(window []json.RawMessage, covered []string) ([]byte, error) {
	raw, err := json.Marshal(savedWindow{Window: window, Covered: covered})
	if err != nil || len(raw) > MaxStateBytes {
		return nil, ErrUnsupported
	}
	return raw, nil
}

func (state *State) sealItem(window []json.RawMessage, covered []string, item json.RawMessage) (json.RawMessage, error) {
	if state == nil || state.Cipher == nil || state.Scope == "" {
		return nil, ErrInvalidState
	}
	raw, err := encodeWindow(window, covered)
	if err != nil {
		return nil, err
	}
	id := itemString(item, "id")
	encrypted, err := state.Cipher.Seal(raw, state.aad(id))
	if err != nil {
		return nil, err
	}
	var value map[string]string
	if json.Unmarshal(item, &value) != nil {
		return nil, ErrInvalidState
	}
	value["encrypted_content"] = statePrefix + base64.RawURLEncoding.EncodeToString(encrypted)
	return json.Marshal(value)
}

func newItem(triggerID string) (json.RawMessage, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return nil, err
	}
	id := "cmp_subpool_" + hex.EncodeToString(random)
	item := map[string]string{"type": "compaction", "id": id, "encrypted_content": ""}
	if triggerID != "" {
		item["created_by"] = triggerID
	}
	return json.Marshal(item)
}

func (state *State) open(item []byte) (savedWindow, error) {
	id, token := itemString(item, "id"), itemString(item, "encrypted_content")
	if state == nil || state.Cipher == nil || state.Scope == "" || len(id) > 256 || !strings.HasPrefix(id, "cmp_subpool_") || !strings.HasPrefix(token, statePrefix) || len(token) > base64.RawURLEncoding.EncodedLen(MaxStateBytes+64)+len(statePrefix) {
		return savedWindow{}, ErrInvalidState
	}
	encrypted, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, statePrefix))
	if err != nil {
		return savedWindow{}, ErrInvalidState
	}
	raw, err := state.Cipher.Open(encrypted, state.aad(id))
	var saved savedWindow
	if err != nil || len(raw) > MaxStateBytes || json.Unmarshal(raw, &saved) != nil || len(saved.Window) == 0 {
		return savedWindow{}, ErrInvalidState
	}
	for _, item := range saved.Window {
		if !supportedItem(item) {
			return savedWindow{}, ErrInvalidState
		}
	}
	return saved, nil
}

func (state *State) aad(id string) []byte {
	// JSON framing prevents scope/ID boundary collisions. The provider and
	// version are domain-separated from stored credential encryption.
	raw, _ := json.Marshal([]string{"subpool/compaction/copilot/v1", state.Scope, id})
	return raw
}

func fingerprint(item []byte) string {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(item))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	delete(value, "id")
	delete(value, "status")
	if value["type"] == nil && value["role"] != nil {
		value["type"] = "message"
	}
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func removeCovered(prefix []json.RawMessage, covered []string) []json.RawMessage {
	counts := make(map[string]int, len(covered))
	for _, hash := range covered {
		counts[hash]++
	}
	remaining := make([]json.RawMessage, 0, len(prefix))
	for _, item := range prefix {
		hash := fingerprint(item)
		if counts[hash] > 0 {
			counts[hash]--
		} else {
			remaining = append(remaining, item)
		}
	}
	return remaining
}
