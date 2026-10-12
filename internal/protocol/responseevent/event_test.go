package responseevent

import "testing"

func TestClassifyLimit(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    LimitKind
	}{
		{name: "typed quota", payload: `{"type":"error","error":{"code":"copilot_quota_exhausted"}}`, want: LimitQuota},
		{name: "failed rate limit", payload: `{"type":"response.failed","response":{"error":{"type":"rate_limit_exceeded"}}}`, want: LimitTemporary},
		{name: "raw Copilot error", payload: `{"error":{"message":"Copilot quota exhausted"}}`, want: LimitQuota},
		{name: "raw string error", payload: `{"error":"too many requests"}`, want: LimitTemporary},
		{name: "non-limit error", payload: `{"type":"error","error":{"code":"invalid_request_error"}}`, want: LimitNone},
		{name: "non-error event", payload: `{"type":"response.output_text.delta","code":"quota_exhausted","message":"quota exhausted"}`, want: LimitNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyLimit([]byte(test.payload)); got != test.want {
				t.Fatalf("ClassifyLimit(%s) = %d, want %d", test.payload, got, test.want)
			}
		})
	}
}
