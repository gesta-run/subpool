package copilotcompact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var ErrStateSeal = errors.New("Subpool could not seal compaction state; no replacement window was installed")

// InbandJob starts streaming once an upstream summary accepts the request.
// Earlier failures retain normal HTTP authentication and failover handling.
type InbandJob struct {
	Model, TriggerID string
	Plan             Plan
	State            *State
	Covered          []string
	Run              func(ctx context.Context, started, sampled func()) (Summary, error)
	Failure          func(error) (code, message string)
}

type inbandResult struct {
	item    json.RawMessage
	summary Summary
	err     error
}

func (job InbandJob) Stream(ctx context.Context) (*http.Response, bool, error) {
	return job.stream(ctx, 30*time.Second)
}

func (job InbandJob) stream(ctx context.Context, interval time.Duration) (*http.Response, bool, error) {
	initial, err := newItem(job.TriggerID)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithCancel(ctx)
	started, workDone := make(chan struct{}), make(chan struct{})
	results := make(chan inbandResult, 1)
	var sampled atomic.Bool
	start := sync.OnceFunc(func() { close(started) })
	go func() {
		defer close(workDone)
		results <- job.run(ctx, initial, start, func() { sampled.Store(true) })
	}()
	select {
	case result := <-results:
		select {
		case <-started:
			// Acceptance wins even when a fast completion or later failure is
			// also ready. Never expose a billable operation as a retryable HTTP
			// rejection merely because the scheduler selected its result first.
			results <- result
		default:
			cancel()
			<-workDone
			if result.err != nil {
				return nil, false, result.err
			}
			return InbandResponse(job.Model, result.item, result.summary), false, nil
		}
	case <-ctx.Done():
		stopInbandWork(cancel, workDone, results)
		return nil, false, ctx.Err()
	case <-started:
	}
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer writer.Close()
		defer stopInbandWork(cancel, workDone, results)
		job.writeStream(ctx, newInbandEvents(writer, job.Model, initial), results, &sampled, interval)
	}()
	return inbandHTTPResponse(&inbandBody{PipeReader: reader, cancel: cancel, done: done}), true, nil
}

func (job InbandJob) run(ctx context.Context, initial json.RawMessage, started, sampled func()) inbandResult {
	summary, err := job.Run(ctx, started, sampled)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return inbandResult{err: err}
	}
	item, err := job.State.sealItem(Window(job.Plan, summary.Text), job.Covered, initial)
	if err != nil {
		err = ErrStateSeal
	}
	return inbandResult{item: item, summary: summary, err: err}
}

func (job InbandJob) writeStream(ctx context.Context, events *inbandEvents, results <-chan inbandResult, sampled *atomic.Bool, interval time.Duration) {
	if events.begin() != nil {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if events.progress(sampled.Swap(false)) != nil {
				return
			}
			// Start the next interval after delivery; slow readers must not
			// cause queued clock ticks to burst progress events back-to-back.
			timer.Reset(interval)
		case result := <-results:
			if result.err == nil {
				_ = events.complete(result.item, result.summary)
			} else {
				closeInbandRejection(result.err)
				code, message := job.Failure(result.err)
				_ = events.fail(code, message)
			}
			return
		}
	}
}

func closeInbandRejection(err error) {
	var rejected *Rejection
	if errors.As(err, &rejected) {
		_ = rejected.Response.Body.Close()
	}
}

func stopInbandWork(cancel context.CancelFunc, done <-chan struct{}, results <-chan inbandResult) {
	cancel()
	<-done
	select {
	case result := <-results:
		closeInbandRejection(result.err)
	default:
	}
}

type inbandBody struct {
	*io.PipeReader
	cancel context.CancelFunc
	done   <-chan struct{}
}

func (body *inbandBody) Close() error {
	body.cancel()
	err := body.PipeReader.Close()
	// Keep work buffers reserved until inference and internal accounting stop.
	<-body.done
	return err
}
