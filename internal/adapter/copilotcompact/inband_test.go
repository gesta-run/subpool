package copilotcompact

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPrepareInbandValidatesTriggerAndRetainsPendingCalls(t *testing.T) {
	raw := []byte(`{"model":"illustrative-model","input":[{"role":"developer","content":"Keep constraints"},{"role":"user","content":"Latest task"},{"type":"function_call","name":"fixture","call_id":"call_fixture","arguments":"{}"},{"type":"compaction_trigger","id":"trigger_fixture"}],"tools":[{"type":"function","name":"fixture"}]}`)
	plan, id, err := PrepareInband(raw)
	if err != nil || id != "trigger_fixture" || len(plan.Tail) != 1 || len(plan.Retained) != 1 || itemString(plan.Latest, "content") != "Latest task" {
		t.Fatalf("invalid in-band plan: id=%s plan=%+v err=%v", id, plan, err)
	}
	for _, input := range []string{
		`[{"type":"compaction_trigger"},{"role":"user","content":"task"}]`,
		`[{"role":"user","content":"task"},{"type":"compaction_trigger"},{"type":"compaction_trigger"}]`,
		`[{"role":"user","content":"task"},{"type":"compaction_trigger","id":123}]`,
		`[{"role":"user","content":"task"},{"type":"compaction_trigger","unexpected":true}]`,
		`[{"role":"user","content":"task"},{"type":"compaction_trigger","type":"compaction_trigger"}]`,
		`[{"role":"user","content":"task"},{"type":"compaction","encrypted_content":"foreign"},{"type":"compaction_trigger"}]`,
		`[{"type":"compaction_trigger"}]`,
	} {
		if _, _, err := PrepareInband([]byte(`{"input":` + input + `}`)); err == nil {
			t.Fatalf("unsafe trigger accepted: %s", input)
		}
	}
	for _, field := range []string{`"previous_response_id":"resp_fixture"`, `"context_management":[{"type":"compaction"}]`, `"compaction_trigger":true`} {
		if _, _, err := PrepareInband([]byte(`{` + field + `,"input":[{"role":"user","content":"task"},{"type":"compaction_trigger"}]}`)); err == nil {
			t.Fatalf("unsupported contract accepted: %s", field)
		}
	}
}

func fixtureInbandJob(t *testing.T, run func(context.Context, func(), func()) (Summary, error)) InbandJob {
	t.Helper()
	return InbandJob{
		Model: "illustrative-model", TriggerID: "illustrative-trigger", State: fixtureState(t, 7, "illustrative-key"),
		Plan: Plan{Latest: json.RawMessage(`{"role":"user","content":"Illustrative task"}`)}, Run: run,
		Failure: func(error) (string, string) { return "illustrative_failure", "No replacement window was installed" },
	}
}

func nextInbandEvent(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()
	for scanner.Scan() {
		if data := strings.TrimPrefix(scanner.Text(), "data: "); data != scanner.Text() {
			var event map[string]any
			if json.Unmarshal([]byte(data), &event) != nil {
				t.Fatal("invalid SSE event")
			}
			return event
		}
	}
	t.Fatalf("missing SSE event: %v", scanner.Err())
	return nil
}

func TestInbandStreamEmitsLifecycleAndThrottledContentFreeProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sample, finish := make(chan struct{}), make(chan struct{})
	job := fixtureInbandJob(t, func(ctx context.Context, started, sampled func()) (Summary, error) {
		started()
		select {
		case <-sample:
			sampled()
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
		select {
		case <-finish:
			return Summary{Text: "Illustrative private summary", InputTokens: 8, OutputTokens: 3}, nil
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
	})
	response, pending, err := job.stream(ctx, 5*time.Millisecond)
	if err != nil || !pending {
		t.Fatalf("could not stream pending work: %v", err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for index, kind := range []string{"response.created", "response.in_progress", "response.output_item.added", "response.in_progress"} {
		event := nextInbandEvent(t, scanner)
		if event["type"] != kind || event["sequence_number"] != float64(index) {
			t.Fatalf("unexpected lifecycle: %#v", event)
		}
	}
	close(sample)
	var progress map[string]any
	for progress == nil {
		event := nextInbandEvent(t, scanner)
		if event["type"] == "response.compaction.compacting" {
			progress = event
		}
	}
	if progress["item_id"] == "" || progress["output_index"] != float64(0) || len(progress) != 4 {
		t.Fatalf("progress leaked content or lost item identity: %#v", progress)
	}
	if event := nextInbandEvent(t, scanner); event["type"] != "response.in_progress" {
		t.Fatalf("unchanged summary reported new sampling: %#v", event)
	}
	close(finish)
	var item map[string]any
	for {
		event := nextInbandEvent(t, scanner)
		switch event["type"] {
		case "response.output_item.done":
			item = event["item"].(map[string]any)
		case "response.completed":
			if item == nil || item["id"] != progress["item_id"] || item["encrypted_content"] == "" {
				t.Fatal("final state did not match the initial item")
			}
			result := event["response"].(map[string]any)
			if len(result["output"].([]any)) != 1 || result["usage"].(map[string]any)["total_tokens"] != float64(11) {
				t.Fatal("terminal result lost state or usage")
			}
			return
		}
	}
}

func TestInbandStreamFailureDoesNotEmitCompletedState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	finish := make(chan struct{})
	job := fixtureInbandJob(t, func(ctx context.Context, started, _ func()) (Summary, error) {
		started()
		select {
		case <-finish:
			return Summary{}, errors.New("illustrative private upstream content")
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
	})
	response, pending, err := job.Stream(ctx)
	if err != nil || !pending {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for index := 0; index < 3; index++ {
		_ = nextInbandEvent(t, scanner)
	}
	close(finish)
	event := nextInbandEvent(t, scanner)
	raw, _ := json.Marshal(event)
	if event["type"] != "response.failed" || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "encrypted_content") || len(event["response"].(map[string]any)["output"].([]any)) != 0 {
		t.Fatalf("failure leaked summary or installed state: %s", raw)
	}
}

func TestInbandStreamCloseCancelsAndWaitsForUnreadWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stopped := make(chan struct{})
	job := fixtureInbandJob(t, func(ctx context.Context, started, _ func()) (Summary, error) {
		started()
		<-ctx.Done()
		close(stopped)
		return Summary{}, ctx.Err()
	})
	response, pending, err := job.Stream(ctx)
	if err != nil || !pending {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("stream closed before inference stopped")
	}
}

func TestInbandStreamEarlyRejectionPreservesHTTPHandling(t *testing.T) {
	body := new(closedBody)
	rejected := &Rejection{Response: &http.Response{StatusCode: 401, Body: body}}
	job := fixtureInbandJob(t, func(context.Context, func(), func()) (Summary, error) { return Summary{}, rejected })
	response, pending, err := job.Stream(context.Background())
	if response != nil || pending || err != rejected || body.closed {
		t.Fatal("early rejection lost its original response or was closed before gateway handling")
	}
	_ = body.Close()
}

func TestInbandStreamAcceptedFailureNeverBecomesRetryableHTTP(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		job := fixtureInbandJob(t, func(_ context.Context, started, _ func()) (Summary, error) {
			started()
			return Summary{}, errors.New("illustrative late failure")
		})
		response, pending, err := job.Stream(context.Background())
		if err != nil || !pending || response == nil {
			t.Fatalf("accepted work escaped streaming error handling: pending=%v error=%v", pending, err)
		}
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if !strings.Contains(string(raw), `"type":"response.failed"`) || strings.Contains(string(raw), `"type":"response.completed"`) {
			t.Fatal("late failure did not end the stream without installing state")
		}
	}
}

func TestInbandStreamSlowReaderDoesNotBurstProgressEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	again, sampledAgain := make(chan struct{}), make(chan struct{})
	job := fixtureInbandJob(t, func(ctx context.Context, started, sampled func()) (Summary, error) {
		started()
		sampled()
		select {
		case <-again:
			sampled()
			close(sampledAgain)
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		}
		<-ctx.Done()
		return Summary{}, ctx.Err()
	})
	const interval = 20 * time.Millisecond
	response, _, err := job.stream(ctx, interval)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for index := 0; index < 3; index++ {
		_ = nextInbandEvent(t, scanner)
	}
	// Let the first progress write block while more sampling occurs.
	time.Sleep(3 * interval)
	close(again)
	<-sampledAgain
	if event := nextInbandEvent(t, scanner); event["type"] != "response.compaction.compacting" {
		t.Fatalf("missing first sample: %#v", event)
	}
	first := time.Now()
	if event := nextInbandEvent(t, scanner); event["type"] != "response.compaction.compacting" {
		t.Fatalf("missing second sample: %#v", event)
	}
	if time.Since(first) < interval/2 {
		t.Fatal("slow reader caused progress events to burst faster than their interval")
	}
}

func TestFeaturesAndInbandResponseLifecycle(t *testing.T) {
	trigger, opaque, own := Features([]byte(`{"input":[{"type":"compaction_trigger"},{"type":"compaction","id":"cmp_subpool_fixture"}]}`))
	if !trigger || !opaque || !own {
		t.Fatal("missing compaction features")
	}
	_, _, own = Features([]byte(`{"input":[{"role":"user","content":"compaction_trigger cmp_subpool_"}]}`))
	if own {
		t.Fatal("plain text was mistaken for opaque state")
	}
	state := fixtureState(t, 7, "illustrative-key")
	item, err := state.Seal([]json.RawMessage{json.RawMessage(`{"role":"user","content":"task"}`)}, nil, "trigger_fixture")
	if err != nil {
		t.Fatal(err)
	}
	response := InbandResponse("illustrative-model", item, Summary{InputTokens: 8, OutputTokens: 3})
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var events []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				t.Fatal("invalid SSE JSON")
			}
			events = append(events, event)
		}
	}
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.completed"}
	if len(events) != len(want) {
		t.Fatalf("wrong event count: %d", len(events))
	}
	for index, kind := range want {
		if events[index]["type"] != kind || events[index]["sequence_number"] != float64(index) {
			t.Fatalf("invalid item lifecycle: %#v", events)
		}
	}
	added, done := events[2]["item"].(map[string]any), events[3]["item"].(map[string]any)
	if added["encrypted_content"] != "" || done["encrypted_content"] == "" || done["created_by"] != "trigger_fixture" {
		t.Fatal("final encrypted item was not delivered in output_item.done")
	}
	completed := events[4]["response"].(map[string]any)
	if len(completed["output"].([]any)) != 1 || completed["usage"].(map[string]any)["total_tokens"] != float64(11) {
		t.Fatal("terminal response lost output or usage")
	}
}
