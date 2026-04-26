package anthropicbackend

import (
	"context"
	"encoding/json"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type StreamClient interface {
	StreamEvents(context.Context, anthropic.Request, anthropic.EventSink) (anthropic.Usage, string, error)
}

func RunTranslatorStream(
	client StreamClient,
	ctx context.Context,
	anthReq anthropic.Request,
	model adaptermodel.ResolvedModel,
	reqID string,
	emit func(adapteropenai.StreamChunk) error,
) (anthropic.Usage, string, string, error) {
	tr := NewStreamTranslator(reqID, model.Alias)
	msgStartPayload, err := json.Marshal(struct {
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}{})
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	msgStartChunks, _, _, _, err := tr.HandleEvent("message_start", msgStartPayload)
	if err != nil {
		return anthropic.Usage{}, "", "", err
	}
	for _, ch := range msgStartChunks {
		if err := emit(ch); err != nil {
			return anthropic.Usage{}, "", "", err
		}
	}

	var streamStopReason string
	anthUsage, _, err := client.StreamEvents(ctx, anthReq, func(ev anthropic.StreamEvent) error {
		if ev.Kind == "stop" {
			streamStopReason = ev.StopReason
			return nil
		}
		evName, payload, ok := StreamEventToTranslatorSSE(ev)
		if !ok {
			return nil
		}
		outChunks, _, _, _, handleErr := tr.HandleEvent(evName, payload)
		if handleErr != nil {
			return handleErr
		}
		for _, ch := range outChunks {
			if err := emit(ch); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}

	mdPayload, err := json.Marshal(struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}{
		Delta: struct {
			StopReason string `json:"stop_reason"`
		}{StopReason: streamStopReason},
		Usage: struct {
			OutputTokens int `json:"output_tokens"`
		}{OutputTokens: anthUsage.OutputTokens},
	})
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	mdChunks, _, _, _, err := tr.HandleEvent("message_delta", mdPayload)
	if err != nil {
		return anthUsage, streamStopReason, "", err
	}
	for _, ch := range mdChunks {
		if err := emit(ch); err != nil {
			return anthUsage, streamStopReason, "", err
		}
	}

	stopChunks, _, finishReason, _, err := tr.HandleEvent("message_stop", []byte("{}"))
	if err != nil {
		return anthropic.Usage{}, streamStopReason, "", err
	}
	for _, ch := range stopChunks {
		if err := emit(ch); err != nil {
			return anthropic.Usage{}, streamStopReason, "", err
		}
	}
	return anthUsage, streamStopReason, finishReason, nil
}
