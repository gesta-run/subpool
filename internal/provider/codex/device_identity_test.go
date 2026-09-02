package codex

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestInstallationIDIsStableAndAccountScoped(t *testing.T) {
	first, err := InstallationID("account-1")
	if err != nil {
		t.Fatal(err)
	}
	repeated, _ := InstallationID("account-1")
	second, _ := InstallationID("account-2")
	if first != repeated || first == second {
		t.Fatalf("IDs = %q, %q, %q", first, repeated, second)
	}
	if want := "1b6b7b36-ce24-43fa-9a63-3d8d25f561f1"; first != want {
		t.Fatalf("ID = %q, want stable vector %q", first, want)
	}
	if len(first) != 36 || first[14] != '4' || !strings.ContainsRune("89ab", rune(first[19])) {
		t.Fatalf("ID is not a UUIDv4 value: %q", first)
	}
	if _, err = InstallationID(""); err == nil {
		t.Fatal("empty account ID was accepted")
	}
}

func TestApplyDeviceIdentityRewritesOnlyInstallationFields(t *testing.T) {
	body := map[string]any{"client_metadata": map[string]any{
		"session_id":              "session-1",
		"x-codex-installation-id": "old-device",
		"x-codex-turn-metadata":   `{"installation_id":"old-device","turn_id":"turn-1"}`,
	}}
	if err := ApplyDeviceIdentity(body, "stable-device"); err != nil {
		t.Fatal(err)
	}
	metadata := body["client_metadata"].(map[string]any)
	if metadata["x-codex-installation-id"] != "stable-device" || metadata["session_id"] != "session-1" {
		t.Fatalf("metadata = %#v", metadata)
	}
	var turn map[string]any
	if err := json.Unmarshal([]byte(metadata["x-codex-turn-metadata"].(string)), &turn); err != nil {
		t.Fatal(err)
	}
	if turn["installation_id"] != "stable-device" || turn["turn_id"] != "turn-1" {
		t.Fatalf("turn metadata = %#v", turn)
	}
}

func TestDeviceIdentityHeadersCloneAndPreserveMalformedMetadata(t *testing.T) {
	source := http.Header{
		"X-Codex-Installation-Id": []string{"old-device"},
		"X-Codex-Turn-Metadata":   []string{`{"installation_id":"old-device","thread_id":"thread-1"}`},
	}
	headers := DeviceIdentityHeaders(source, "stable-device")
	if source.Get("X-Codex-Installation-Id") != "old-device" || headers.Get("X-Codex-Installation-Id") != "stable-device" {
		t.Fatalf("source=%#v rewritten=%#v", source, headers)
	}
	var turn map[string]any
	if err := json.Unmarshal([]byte(headers.Get("X-Codex-Turn-Metadata")), &turn); err != nil {
		t.Fatal(err)
	}
	if turn["installation_id"] != "stable-device" || turn["thread_id"] != "thread-1" {
		t.Fatalf("turn metadata = %#v", turn)
	}

	malformed := http.Header{"X-Codex-Turn-Metadata": []string{"not-json"}}
	if got := DeviceIdentityHeaders(malformed, "stable-device").Get("X-Codex-Turn-Metadata"); got != "not-json" {
		t.Fatalf("malformed metadata = %q", got)
	}
}

func TestApplyDeviceIdentityRejectsNonObjectClientMetadata(t *testing.T) {
	err := ApplyDeviceIdentity(map[string]any{"client_metadata": "invalid"}, "stable-device")
	if !errors.Is(err, ErrInvalidClientMetadata) {
		t.Fatalf("error = %v", err)
	}
}
