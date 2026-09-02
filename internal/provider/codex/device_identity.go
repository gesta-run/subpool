package codex

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	installationIDDomain = "subpool/codex/installation-id/v1"
	installationIDHeader = "X-Codex-Installation-Id"
	turnMetadataHeader   = "X-Codex-Turn-Metadata"
)

var ErrInvalidClientMetadata = errors.New("client_metadata must be an object")

func InstallationID(providerAccountID string) (string, error) {
	if strings.TrimSpace(providerAccountID) == "" {
		return "", errors.New("provider account ID is empty")
	}
	digest := sha256.Sum256([]byte(installationIDDomain + "\x00" + providerAccountID))
	raw := digest[:16]
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), nil
}

func ApplyDeviceIdentity(body map[string]any, installationID string) error {
	metadata, err := clientMetadata(body)
	if err != nil {
		return err
	}
	metadata[strings.ToLower(installationIDHeader)] = installationID
	if raw, ok := metadata[strings.ToLower(turnMetadataHeader)].(string); ok {
		if rewritten, valid := rewriteTurnMetadata(raw, installationID); valid {
			metadata[strings.ToLower(turnMetadataHeader)] = rewritten
		}
	}
	return nil
}

func DeviceIdentityHeaders(source http.Header, installationID string) http.Header {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set(installationIDHeader, installationID)
	if raw := headers.Get(turnMetadataHeader); raw != "" {
		if rewritten, valid := rewriteTurnMetadata(raw, installationID); valid {
			headers.Set(turnMetadataHeader, rewritten)
		}
	}
	return headers
}

func clientMetadata(body map[string]any) (map[string]any, error) {
	raw, exists := body["client_metadata"]
	if !exists || raw == nil {
		metadata := make(map[string]any)
		body["client_metadata"] = metadata
		return metadata, nil
	}
	metadata, ok := raw.(map[string]any)
	if !ok {
		return nil, ErrInvalidClientMetadata
	}
	return metadata, nil
}

func rewriteTurnMetadata(raw, installationID string) (string, bool) {
	var metadata map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &metadata) != nil || metadata == nil {
		return "", false
	}
	encodedID, _ := json.Marshal(installationID)
	metadata["installation_id"] = encodedID
	rewritten, err := json.Marshal(metadata)
	if err != nil {
		return "", false
	}
	return string(rewritten), true
}
