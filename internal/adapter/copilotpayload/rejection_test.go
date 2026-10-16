package copilotpayload

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestInspectExplicitCodesOnly(t *testing.T) {
	for _, test := range []struct {
		body string
		want Reason
	}{
		{`{"error":{"code":"context_length_exceeded","message":"illustrative private text"}}`, Context},
		{`{"code":"context_window_exceeded"}`, Context},
		{`{"error":{"code":"payload_too_large"}}`, Body},
		{`{"error":{"code":"image_too_large"}}`, Images},
		{`{"error":{"message":"context_length_exceeded"}}`, Unknown},
		{`{"error":{"code":"unknown","message":"context window exceeded"}}`, Unknown},
		{`{"error":{"code":"context_length_exceeded"}} trailing`, Unknown},
		{`{"error":{"code":"context_length_exceeded","details":"` + strings.Repeat("x", maxErrorBytes) + `"}}`, Unknown},
	} {
		response := &http.Response{StatusCode: 413, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(test.body))}
		if got := Inspect(response); got != test.want {
			t.Fatalf("got %s, want %s", got, test.want)
		}
		raw, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(raw) != test.body {
			t.Fatal("inspection consumed or changed the original response")
		}
	}
}

func TestMeasureContainsOnlySerializedSizes(t *testing.T) {
	image := `{"type":"input_image","image_url":"data:image/png;base64,illustrative"}`
	toolOutput := fmt.Sprintf(`{"type":"function_call_output","call_id":"call_illustrative","output":[%s]}`, image)
	request := fmt.Sprintf(`{"instructions":"fixture","tools":[{"type":"function","name":"fixture"}],"input":[{"role":"user","content":[%s]},%s]}`, image, toolOutput)
	sizes := Measure([]byte(request))
	if sizes.Instructions != len(`"fixture"`) || sizes.Tools == 0 || sizes.Messages == 0 || sizes.ToolOutputs != len(toolOutput) || sizes.Images != 2*len(image) {
		t.Fatalf("incorrect sizes: %+v", sizes)
	}
	chat := Measure([]byte(`{"messages":[{"role":"tool","content":"illustrative output"}]}`))
	if chat.ToolOutputs == 0 || chat.Messages != 0 || chat.Images != 0 {
		t.Fatalf("incorrect chat sizes: %+v", chat)
	}
}
