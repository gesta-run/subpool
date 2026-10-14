package bailian

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (c *Client) accessToken(ctx context.Context, credentials Credentials) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://modelstudio.cn-beijing.aliyuncs.com/modelstudio/cli/generateAccessToken", nil)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	signRequest(req, credentials, time.Now().UTC().Format(time.RFC3339), hex.EncodeToString(nonce))
	body, err := c.request(req)
	if err != nil {
		return "", err
	}
	var result struct {
		Token string `json:"cliAccessToken"`
	}
	if json.Unmarshal(body, &result) != nil || result.Token == "" {
		return "", ErrPermissionDenied
	}
	return result.Token, nil
}

// ACS3 signing follows the official Bailian CLI's GenerateCLIAccessToken request.
func signRequest(req *http.Request, credentials Credentials, date, nonce string) {
	headers := map[string]string{
		"host":                  req.URL.Host,
		"content-type":          "application/json",
		"x-acs-action":          "GenerateCLIAccessToken",
		"x-acs-version":         "2026-02-10",
		"x-acs-date":            date,
		"x-acs-signature-nonce": nonce,
		"x-acs-content-sha256":  hash(""),
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var canonical strings.Builder
	for _, key := range keys {
		canonical.WriteString(key + ":" + headers[key] + "\n")
		req.Header.Set(key, headers[key])
	}
	signedHeaders := strings.Join(keys, ";")
	request := strings.Join([]string{req.Method, req.URL.Path, req.URL.RawQuery, canonical.String(), signedHeaders, hash("")}, "\n")
	mac := hmac.New(sha256.New, []byte(credentials.AccessKeySecret))
	mac.Write([]byte("ACS3-HMAC-SHA256\n" + hash(request)))
	req.Header.Set("Authorization", "ACS3-HMAC-SHA256 Credential="+credentials.AccessKeyID+",SignedHeaders="+signedHeaders+",Signature="+hex.EncodeToString(mac.Sum(nil)))
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
