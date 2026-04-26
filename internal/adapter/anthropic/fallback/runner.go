package fallback

import (
	"context"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type CollectOpenAIInput struct {
	RequestID         string
	ModelAlias        string
	SystemFingerprint string
	CoerceText        TextCoercer
}

type CollectOpenAIResult struct {
	Final FinalResponse
	Raw   Result
}

func (c *Client) CollectOpenAI(ctx context.Context, req Request, in CollectOpenAIInput) (CollectOpenAIResult, error) {
	raw, err := c.Collect(ctx, req)
	if err != nil {
		return CollectOpenAIResult{}, err
	}
	final := BuildFinalResponse(FinalResponseInput{
		Request:           req,
		Result:            raw,
		RequestID:         in.RequestID,
		ModelAlias:        in.ModelAlias,
		SystemFingerprint: in.SystemFingerprint,
		CoerceText:        in.CoerceText,
	})
	return CollectOpenAIResult{Final: final, Raw: raw}, nil
}

type StreamOpenAIInput struct {
	RequestID  string
	ModelAlias string
	Created    int64
}

type StreamOpenAIResult struct {
	Plan     StreamPlan
	Raw      StreamResult
	Buffered bool
}

func (c *Client) StreamOpenAI(ctx context.Context, req Request, in StreamOpenAIInput, emit func(adapteropenai.StreamChunk) error) (StreamOpenAIResult, error) {
	buffered := ShouldBufferTools(req)
	var raw StreamResult
	var err error
	if buffered {
		raw, err = c.Stream(ctx, req, func(StreamEvent) error { return nil })
	} else {
		firstDelta := true
		raw, err = c.Stream(ctx, req, func(ev StreamEvent) error {
			chunk, ok := BuildLiveStreamChunk(in.RequestID, in.ModelAlias, in.Created, ev, firstDelta)
			if !ok {
				return nil
			}
			firstDelta = false
			return emit(chunk)
		})
	}
	plan := BuildStreamPlan(StreamPlanInput{
		Request:     req,
		Result:      raw,
		RequestID:   in.RequestID,
		ModelAlias:  in.ModelAlias,
		Created:     in.Created,
		BufferedRun: buffered,
	})
	for _, chunk := range plan.Chunks {
		_ = emit(chunk)
	}
	return StreamOpenAIResult{Plan: plan, Raw: raw, Buffered: buffered}, err
}
