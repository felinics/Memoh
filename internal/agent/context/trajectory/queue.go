package trajectory

import (
	"context"
	"log/slog"
	"time"
)

type captureJob struct {
	ctx            context.Context
	event          Event
	contents       []Content
	encodingErrors int64
	done           chan struct{}
}

func (r *Recorder) Record(ctx context.Context, stage string, stepIndex *int, blocks ...Block) int64 {
	return r.record(ctx, stage, stepIndex, blocks, true)
}

func (r *Recorder) RecordAsync(ctx context.Context, stage string, stepIndex *int, blocks ...Block) int64 {
	return r.record(ctx, stage, stepIndex, blocks, false)
}

func (r *Recorder) record(ctx context.Context, stage string, stepIndex *int, blocks []Block, wait bool) int64 {
	if r == nil || r.sink == nil {
		return 0
	}
	r.recordMu.Lock()
	job := r.prepareCapture(ctx, stage, stepIndex, blocks)
	if job == nil {
		r.recordMu.Unlock()
		return 0
	}
	r.mu.Lock()
	r.jobs = append(r.jobs, job)
	r.stats.Pending++
	r.last = job.done
	start := !r.working
	r.working = true
	r.mu.Unlock()
	r.recordMu.Unlock()
	if start {
		go r.drain(ctx)
	}
	if wait {
		<-job.done
	}
	return job.event.Sequence
}

func (r *Recorder) drain(ctx context.Context) {
	for {
		r.mu.Lock()
		if len(r.jobs) == 0 {
			r.jobs = nil
			r.working = false
			r.mu.Unlock()
			return
		}
		job := r.jobs[0]
		r.jobs[0] = nil
		r.jobs = r.jobs[1:]
		r.stats.Errors += job.encodingErrors
		job.event.CaptureErrors = r.stats.Errors
		r.mu.Unlock()
		writeCtx, cancel := context.WithTimeout(job.ctx, 5*time.Second)
		err := appendCapture(writeCtx, r.sink, job.event, job.contents) //nolint:contextcheck // Each queued capture retains its own request context.
		cancel()
		r.mu.Lock()
		if err != nil {
			r.stats.Errors++
		}
		r.stats.Pending--
		r.mu.Unlock()
		if err != nil {
			r.logger.WarnContext(ctx, "context trajectory capture failed", slog.String("run_id", job.event.RunID), slog.String("stage", job.event.Stage), slog.Int64("sequence", job.event.Sequence), slog.Any("error", err))
		}
		close(job.done)
	}
}

func (r *Recorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	last := r.last
	r.mu.Unlock()
	if last == nil {
		return nil
	}
	select {
	case <-last:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
