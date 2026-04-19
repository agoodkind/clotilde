package adapter

import (
	"reflect"
	"testing"
)

func TestRedactedHeader(t *testing.T) {
	cases := []struct {
		name     string
		header   string
		expected bool
	}{
		{name: "authorization", header: "authorization", expected: true},
		{name: "proxy-authorization", header: "proxy-authorization", expected: true},
		{name: "cookie", header: "cookie", expected: true},
		{name: "set-cookie", header: "set-cookie", expected: true},
		{name: "x-clyde-token", header: "x-clyde-token", expected: true},
		{name: "x-cursor-session", header: "x-cursor-session", expected: true},
		{name: "x-cursor-version", header: "x-cursor-version", expected: true},
		{name: "openai-api-key", header: "openai-api-key", expected: true},
		{name: "openai-organization", header: "openai-organization", expected: true},
		{name: "x-amz-security-token", header: "x-amz-security-token", expected: true},
		{name: "x-custom-api-key", header: "x-custom-api-key", expected: true},
		{name: "content-type", header: "content-type", expected: false},
		{name: "x-request-id", header: "x-request-id", expected: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactedHeader(tc.header)
			if got != tc.expected {
				t.Fatalf("redactedHeader(%q) = %v want %v", tc.header, got, tc.expected)
			}
		})
	}
}

func TestRedactedHeaders(t *testing.T) {
	headers := map[string][]string{
		"Authorization":        {"redactme"},
		"Content-Type":         {"application/json"},
		"X-AMZ-Security-Token": {"redactme"},
		"X-Cursor-Secret":      {"value"},
		"OpenAI-Token":         {"value"},
		"User-Agent":           {"ua"},
	}

	out := redactedHeaders(headers)
	expected := map[string]string{
		"authorization":        "[redacted]",
		"x-amz-security-token": "[redacted]",
		"content-type":         "application/json",
		"x-cursor-secret":      "[redacted]",
		"openai-token":         "[redacted]",
		"user-agent":           "ua",
	}
	if !reflect.DeepEqual(out, expected) {
		t.Fatalf("redactedHeaders = %#v want %#v", out, expected)
	}
}
