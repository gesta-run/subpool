package copilotcompact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type closedBody struct{ closed bool }

func (*closedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (body *closedBody) Close() error        { body.closed = true; return nil }

func TestRunBoundsChunksAndCarriesSummary(t *testing.T) {
	plan := Plan{Model: "fixture", Parts: []json.RawMessage{textPart(strings.Repeat("中文\\\"", ChunkBytes/3))}}
	calls := 0
	previous := ""
	result, err := Run(context.Background(), plan, func(_ context.Context, raw []byte) (Summary, error) {
		calls++
		if !json.Valid(raw) || len(raw) > 4*ChunkBytes {
			t.Fatal("invalid or unbounded summary request")
		}
		if previous != "" && !strings.Contains(string(raw), previous) {
			t.Fatal("a later chunk lost the previous summary")
		}
		previous = "illustrative rolling summary"
		return Summary{Text: previous, InputTokens: 5, OutputTokens: 2}, nil
	})
	if err != nil || calls < 2 || result.InputTokens != int64(calls)*5 || result.OutputTokens != int64(calls)*2 {
		t.Fatalf("calls=%d summary=%+v error=%v", calls, result, err)
	}
}

func TestRunCapacityRetrySplitsInsteadOfReplaying(t *testing.T) {
	body := new(closedBody)
	calls := 0
	firstSize := 0
	result, err := Run(context.Background(), Plan{Parts: []json.RawMessage{textPart(strings.Repeat("fixture ", 1000))}}, func(_ context.Context, raw []byte) (Summary, error) {
		calls++
		if calls == 1 {
			firstSize = len(raw)
			return Summary{}, &Rejection{Response: &http.Response{StatusCode: 413, Body: body}, Capacity: true}
		}
		if len(raw) >= firstSize {
			t.Fatal("capacity retry did not reduce the failed input")
		}
		return Summary{Text: "fixture summary"}, nil
	})
	if err != nil || result.Text == "" || calls != 3 || !body.closed {
		t.Fatalf("calls=%d closed=%v error=%v", calls, body.closed, err)
	}
}

func TestRunRejectsPartialEmptyAndUnboundedWork(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("x", MaxSummaryBytes+1)} {
		result, err := Run(context.Background(), Plan{Parts: []json.RawMessage{textPart("fixture")}}, func(context.Context, []byte) (Summary, error) {
			return Summary{Text: value}, nil
		})
		if err == nil || result.Text != "" {
			t.Fatal("invalid summary could be installed")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Plan{Parts: []json.RawMessage{textPart("fixture")}}, func(context.Context, []byte) (Summary, error) {
		t.Fatal("canceled compaction called upstream")
		return Summary{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestRunLeavesTerminalRejectionsOpenForCaller(t *testing.T) {
	for _, capacity := range []bool{false, true} {
		body := new(closedBody)
		_, err := Run(context.Background(), Plan{Parts: []json.RawMessage{textPart("small fixture")}}, func(context.Context, []byte) (Summary, error) {
			return Summary{}, &Rejection{Response: &http.Response{StatusCode: 403, Body: body}, Capacity: capacity}
		})
		if err == nil || body.closed {
			t.Fatal("terminal upstream response was swallowed or prematurely closed")
		}
	}
}

func TestRunHonorsCancellationAfterFinalSummary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result, err := Run(ctx, Plan{Parts: []json.RawMessage{textPart("fixture")}}, func(context.Context, []byte) (Summary, error) {
		cancel()
		return Summary{Text: "fixture summary"}, nil
	})
	if !errors.Is(err, context.Canceled) || result.Text != "" {
		t.Fatal("canceled final summary could be installed")
	}
}

func TestRunLargeImagesStayAtomicAndBounded(t *testing.T) {
	image, _ := json.Marshal(map[string]string{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("a", ChunkBytes*2)})
	calls := 0
	_, err := Run(context.Background(), Plan{Parts: []json.RawMessage{textPart("fixture image context"), image}}, func(_ context.Context, body []byte) (Summary, error) {
		calls++
		if calls == 2 && (!strings.Contains(string(body), string(image)) || strings.Contains(string(body), `\"image_url\"`)) {
			t.Fatal("image became text or was split")
		}
		return Summary{Text: "fixture summary"}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("bounded image failed: calls=%d error=%v", calls, err)
	}
}

func TestRunRejectsUnboundedWorkBeforeInference(t *testing.T) {
	part := textPart(strings.Repeat("x", ChunkBytes/2))
	plan := Plan{Parts: make([]json.RawMessage, MaxCalls+1)}
	for index := range plan.Parts {
		plan.Parts[index] = part
	}
	_, err := Run(context.Background(), plan, func(context.Context, []byte) (Summary, error) {
		t.Fatal("unbounded work reached upstream")
		return Summary{}, nil
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("work limit was not enforced: %v", err)
	}
}
