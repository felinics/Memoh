package native

import sdk "github.com/memohai/twilight-ai/sdk"

// stepMessageCapture retains context messages appended by PrepareStep. They
// are input to the next model call, so they become durable only with that
// call's complete step.
type stepMessageCapture struct {
	byStep map[int][]sdk.Message
}

func (c *stepMessageCapture) messages(stepIndex int) []sdk.Message {
	if c == nil {
		return nil
	}
	return append([]sdk.Message(nil), c.byStep[stepIndex]...)
}

func capturePreparedStepMessages(prepare func(*sdk.GenerateParams) *sdk.GenerateParams) (func(*sdk.GenerateParams) *sdk.GenerateParams, *stepMessageCapture) {
	capture := &stepMessageCapture{byStep: make(map[int][]sdk.Message)}
	if prepare == nil {
		return nil, capture
	}
	nextStep := 1 // Twilight calls PrepareStep only before steps after step zero.
	return func(params *sdk.GenerateParams) *sdk.GenerateParams {
		before := len(params.Messages)
		override := prepare(params)
		actual := params
		if override != nil {
			actual = override
		}
		if len(actual.Messages) > before {
			capture.byStep[nextStep] = append([]sdk.Message(nil), actual.Messages[before:]...)
		}
		nextStep++
		return override
	}, capture
}

func (c *stepMessageCapture) decorate(stepIndex int, step *sdk.StepResult, metadata *toolExecutionMetadataRegistry) *sdk.StepResult {
	if step == nil {
		return nil
	}
	decorated := *step
	decorated.Messages = append(c.messages(stepIndex), step.Messages...)
	if decorated.DeferredToolApproval != nil {
		decorated.Messages = annotateDeferredApproval(decorated.Messages, *decorated.DeferredToolApproval)
	}
	decorated.Messages = metadata.annotate(decorated.Messages)
	return &decorated
}
