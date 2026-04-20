package chatemit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type noticeClaimer func(kind string, resetsAt time.Time) bool
type noticeUnclaimer func(kind string, resetsAt time.Time)
type encodeJSON func(any) ([]byte, error)

// EvaluateNoticeFromHeaders checks the Anthropic headers and claims a notice slot.
func EvaluateNoticeFromHeaders(h http.Header, noticesEnabled bool, claim noticeClaimer) *anthropic.Notice {
	if !noticesEnabled {
		return nil
	}
	notice := anthropic.EvaluateNotice(h, time.Now().UTC())
	if notice == nil || claim == nil {
		return nil
	}
	if strings.TrimSpace(notice.Text) == "" {
		return nil
	}
	if !claim(notice.Kind, notice.ResetsAt) {
		return nil
	}
	return notice
}

func noticeSentinelText(text string) string {
	return fmt.Sprintf("<!--clyde-notice-->[System message: %s]<!--/clyde-notice-->\n\n", text)
}

func openAINoticeChunk(reqID, modelAlias, text string) tooltrans.OpenAIStreamChunk {
	return tooltrans.OpenAIStreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelAlias,
		Choices: []tooltrans.OpenAIStreamChoice{{
			Index: 0,
			Delta: tooltrans.OpenAIStreamDelta{
				Role:    "assistant",
				Content: text,
			},
		}},
	}
}

// NoticeForStreamHeaders evaluates headers, emits one notice chunk, and
// unclaims the slot when emission fails.
func NoticeForStreamHeaders(
	reqID string,
	modelAlias string,
	h http.Header,
	noticesEnabled bool,
	emit func(tooltrans.OpenAIStreamChunk) error,
	claim noticeClaimer,
	unclaim noticeUnclaimer,
) (*anthropic.Notice, error) {
	if emit == nil {
		return nil, nil
	}
	notice := EvaluateNoticeFromHeaders(h, noticesEnabled, claim)
	if notice == nil {
		return nil, nil
	}
	chunk := openAINoticeChunk(reqID, modelAlias, noticeSentinelText(notice.Text))
	if err := emit(chunk); err != nil {
		if unclaim != nil {
			unclaim(notice.Kind, notice.ResetsAt)
		}
		return notice, err
	}
	return nil, nil
}

// NoticeForResponseHeaders injects a notice into the first assistant message and
// unclaims the slot when injection fails.
func NoticeForResponseHeaders(
	resp ChatResponse,
	notice *anthropic.Notice,
	unclaim noticeUnclaimer,
	encode encodeJSON,
) (ChatResponse, bool) {
	return prependNoticeToResponse(resp, notice, unclaim, encode)
}

func prependNoticeToResponse(resp ChatResponse, notice *anthropic.Notice, unclaim noticeUnclaimer, encode encodeJSON) (ChatResponse, bool) {
	if notice == nil || len(resp.Choices) == 0 {
		return resp, false
	}
	var content string
	if err := json.Unmarshal(resp.Choices[0].Message.Content, &content); err != nil {
		if unclaim != nil {
			unclaim(notice.Kind, notice.ResetsAt)
		}
		return resp, false
	}
	content = noticeSentinelText(notice.Text) + content
	if encode == nil {
		encode = json.Marshal
	}
	encoded, err := encode(content)
	if err != nil {
		if unclaim != nil {
			unclaim(notice.Kind, notice.ResetsAt)
		}
		return resp, false
	}
	resp.Choices[0].Message.Content = json.RawMessage(encoded)
	return resp, true
}
