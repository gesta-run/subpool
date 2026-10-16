package copilotcompact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/credential"
)

func fixtureState(t *testing.T, key byte, scope string) *State {
	t.Helper()
	cipher, err := credential.New(bytes.Repeat([]byte{key}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return &State{Cipher: cipher, Scope: scope}
}

func TestStateIsScopedAuthenticatedAndRestartSafe(t *testing.T) {
	state := fixtureState(t, 7, "illustrative-key")
	window := []json.RawMessage{json.RawMessage(`{"role":"user","content":"Illustrative retained task"}`)}
	item, err := state.Seal(window, []string{"covered-fixture"}, "trigger-fixture")
	if err != nil || bytes.Contains(item, []byte("Illustrative retained")) {
		t.Fatalf("state was not encrypted: %v", err)
	}
	restarted := fixtureState(t, 7, "illustrative-key")
	saved, err := restarted.open(item)
	if err != nil || len(saved.Window) != 1 || itemString(item, "created_by") != "trigger-fixture" {
		t.Fatalf("state could not survive restart: %v", err)
	}
	for _, other := range []*State{nil, fixtureState(t, 7, "other-key"), fixtureState(t, 8, "illustrative-key")} {
		if _, err := other.open(item); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("foreign state was accepted: %v", err)
		}
	}
	var tampered map[string]string
	_ = json.Unmarshal(item, &tampered)
	tampered["id"] += "changed"
	raw, _ := json.Marshal(tampered)
	if _, err := state.open(raw); !errors.Is(err, ErrInvalidState) {
		t.Fatal("the item ID was not authenticated")
	}
	tampered["id"] = itemString(item, "id")
	token := []byte(tampered["encrypted_content"])
	token[len(statePrefix)+10] ^= 1
	tampered["encrypted_content"] = string(token)
	raw, _ = json.Marshal(tampered)
	if _, err := state.open(raw); !errors.Is(err, ErrInvalidState) {
		t.Fatal("tampered encrypted content was accepted")
	}
	legacy, _ := state.Cipher.Seal([]byte(`{"window":[]}`), nil)
	if _, err := state.Cipher.Open(legacy, state.aad(itemString(item, "id"))); err == nil {
		t.Fatal("credential encryption was not domain separated")
	}
}

func TestStateRestoreDeduplicatesCoveredPrefixWithoutDroppingNewItems(t *testing.T) {
	state := fixtureState(t, 7, "illustrative-key")
	original := []byte(`{"input":[{"type":"message","id":null,"role":"user","content":"Initial task"},{"role":"user","content":"Latest task"},{"type":"compaction_trigger"}]}`)
	covered, err := Coverage(original)
	if err != nil {
		t.Fatal(err)
	}
	window := []json.RawMessage{json.RawMessage(`{"role":"user","content":"Latest task"}`), json.RawMessage(`{"role":"assistant","content":"Earlier reference summary"}`)}
	item, err := state.Seal(window, covered, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"input":[{"role":"developer","content":"New constraint"},{"content":"Initial task","role":"user"},{"role":"user","content":"Latest task"},` + string(item) + `,{"role":"user","content":"Continue"}],"tools":[{"type":"function","name":"fixture"}]}`)
	restored, err := state.Restore(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(restored, []byte("encrypted_content")) || bytes.Contains(restored, []byte("Initial task")) || bytes.Count(restored, []byte("Latest task")) != 1 {
		t.Fatalf("covered history was duplicated: %s", restored)
	}
	for _, retained := range []string{"New constraint", "Earlier reference summary", "Continue", `"tools"`} {
		if !bytes.Contains(restored, []byte(retained)) {
			t.Fatalf("lost %q", retained)
		}
	}
	if bytes.Index(restored, []byte("New constraint")) < bytes.Index(restored, []byte("Earlier reference summary")) {
		t.Fatal("new instructions lost precedence over the sealed snapshot")
	}
	// A client may install only the opaque item, or keep an identical message
	// beyond the covered count. Neither case may lose its retained latest task.
	for _, prefix := range []string{"", `{"role":"user","content":"Latest task"},{"role":"user","content":"Latest task"},`} {
		restored, err = state.Restore([]byte(`{"input":[` + prefix + string(item) + `]}`))
		want := 1
		if prefix != "" {
			want = 2
		}
		if err != nil || bytes.Count(restored, []byte("Latest task")) != want {
			t.Fatalf("restore lost repeated messages: %v", err)
		}
	}
	if _, err := state.Restore([]byte(`{"input":[` + string(item) + `,` + string(item) + `]}`)); !errors.Is(err, ErrInvalidState) {
		t.Fatal("multiple opaque windows were allowed to amplify input")
	}
}

func TestStateRejectsUnboundedOrForeignTokens(t *testing.T) {
	state := fixtureState(t, 7, "illustrative-key")
	for _, item := range []string{
		`{"type":"compaction","id":"cmp_foreign","encrypted_content":"foreign"}`,
		`{"type":"compaction","id":"cmp_subpool_fixture"}`,
		`{"type":"compaction","id":"cmp_subpool_fixture","encrypted_content":"` + statePrefix + strings.Repeat("A", 2*MaxStateBytes) + `"}`,
	} {
		if _, err := state.Restore([]byte(`{"input":[` + item + `]}`)); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("foreign or excessive token accepted: %v", err)
		}
	}
	large, _ := json.Marshal(map[string]string{"role": "user", "content": strings.Repeat("x", MaxStateBytes)})
	if _, err := state.Seal([]json.RawMessage{large}, nil, ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unbounded retained window was accepted")
	}
}

func TestStateSizePreflightCoversWorstCaseSummaryEncoding(t *testing.T) {
	state := fixtureState(t, 7, "illustrative-key")
	covered := []string{"illustrative-covered-fingerprint"}
	user := func(size int) json.RawMessage {
		raw, _ := json.Marshal(map[string]string{"role": "user", "content": strings.Repeat("x", size)})
		return raw
	}
	plan := Plan{Latest: user(0), Parts: []json.RawMessage{textPart("Illustrative history")}}
	maxSummary := strings.Repeat("\x00", MaxSummaryBytes)
	base, err := encodeWindow(Window(plan, maxSummary), covered)
	if err != nil {
		t.Fatal(err)
	}
	capacity := MaxStateBytes - len(base)
	plan.Latest = user(capacity)
	if err := ValidateStateSize(plan, covered); err != nil {
		t.Fatalf("boundary window was rejected: %v", err)
	}
	for _, character := range []string{"\x00", "<", ">", "&", "\"", "\\"} {
		if _, err := state.Seal(Window(plan, strings.Repeat(character, MaxSummaryBytes)), covered, ""); err != nil {
			t.Fatalf("accepted window could not seal escaped summary %q: %v", character, err)
		}
	}
	plan.Latest = user(capacity + 1)
	if err := ValidateStateSize(plan, covered); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("oversized worst-case window passed preflight: %v", err)
	}
	// A no-op compaction does not reserve space for a nonexistent summary.
	plan.Parts, plan.Latest = nil, user(0)
	base, _ = encodeWindow(Window(plan, ""), covered)
	plan.Latest = user(MaxStateBytes - len(base))
	if err := ValidateStateSize(plan, covered); err != nil {
		t.Fatalf("unchanged boundary window was rejected: %v", err)
	}
	if _, err := state.Seal(Window(plan, ""), covered, ""); err != nil {
		t.Fatal(err)
	}
}
