package trajectory

import "context"

type recorderKey struct{}

type requestKey struct{}

func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	return WithRequest(context.WithValue(ctx, recorderKey{}, recorder), 0)
}

func FromContext(ctx context.Context) *Recorder {
	recorder, _ := ctx.Value(recorderKey{}).(*Recorder)
	return recorder
}

func WithRequest(ctx context.Context, sequence int64) context.Context {
	return context.WithValue(ctx, requestKey{}, sequence)
}

func requestFromContext(ctx context.Context) int64 {
	sequence, _ := ctx.Value(requestKey{}).(int64)
	return sequence
}
