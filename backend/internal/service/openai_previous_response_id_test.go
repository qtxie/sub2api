package service

import (
	"strings"
	"testing"
)

func TestClassifyOpenAIPreviousResponseIDKind(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "empty", id: " ", want: OpenAIPreviousResponseIDKindEmpty},
		{name: "response_id", id: "resp_0906a621bc423a8d0169a108637ef88197b74b0e2f37ba358f", want: OpenAIPreviousResponseIDKindResponseID},
		{name: "message_id", id: "msg_123456", want: OpenAIPreviousResponseIDKindMessageID},
		{name: "item_id", id: "item_abcdef", want: OpenAIPreviousResponseIDKindMessageID},
		{name: "case insensitive message prefix", id: "CHATCMPL_AbC123", want: OpenAIPreviousResponseIDKindMessageID},
		{name: "trimmed response id", id: "  resp_123  ", want: OpenAIPreviousResponseIDKindResponseID},
		{name: "maximum response id suffix", id: "resp_" + strings.Repeat("a", 256), want: OpenAIPreviousResponseIDKindResponseID},
		{name: "response id suffix too long", id: "resp_" + strings.Repeat("a", 257), want: OpenAIPreviousResponseIDKindUnknown},
		{name: "empty response suffix", id: "resp_", want: OpenAIPreviousResponseIDKindUnknown},
		{name: "response prefix is case sensitive", id: "RESP_123", want: OpenAIPreviousResponseIDKindUnknown},
		{name: "unknown", id: "foo_123456", want: OpenAIPreviousResponseIDKindUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyOpenAIPreviousResponseIDKind(tc.id); got != tc.want {
				t.Fatalf("ClassifyOpenAIPreviousResponseIDKind(%q)=%q want=%q", tc.id, got, tc.want)
			}
		})
	}
}

func TestIsOpenAIPreviousResponseIDLikelyMessageID(t *testing.T) {
	if !IsOpenAIPreviousResponseIDLikelyMessageID("msg_123") {
		t.Fatal("expected msg_123 to be identified as message id")
	}
	if IsOpenAIPreviousResponseIDLikelyMessageID("resp_123") {
		t.Fatal("expected resp_123 not to be identified as message id")
	}
}

func TestIsOpenAIResponseReplayableWithoutPreviousID(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "plain string input", body: `{"input":"hello"}`, want: true},
		{name: "empty string input", body: `{"input":"  "}`, want: false},
		{name: "input text item", body: `{"input":[{"type":"input_text","text":"hello"}]}`, want: true},
		{name: "message string content", body: `{"input":[{"type":"message","role":"user","content":"hello"}]}`, want: true},
		{name: "message content item", body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`, want: true},
		{name: "single input object", body: `{"input":{"type":"input_text","text":"hello"}}`, want: true},
		{name: "string item in input array", body: `{"input":["hello"]}`, want: true},
		{name: "single message content object", body: `{"input":{"type":"message","content":{"type":"text","text":"hello"}}}`, want: true},
		{name: "image input item", body: `{"input":[{"type":"input_image","image_url":"https://example.test/image.png"}]}`, want: true},
		{name: "image input file id", body: `{"input":[{"type":"input_image","file_id":"file_123"}]}`, want: true},
		{name: "message image file id", body: `{"input":[{"type":"message","content":[{"type":"input_image","file_id":"file_123"}]}]}`, want: true},
		{name: "later replayable array item", body: `{"input":[{"type":"reasoning","encrypted_content":"gAAA"},{"type":"input_text","text":"hello"}]}`, want: true},
		{name: "function call output", body: `{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`, want: false},
		{name: "reasoning encrypted only", body: `{"input":[{"type":"reasoning","encrypted_content":"gAAA"}]}`, want: false},
		{name: "image without source", body: `{"input":[{"type":"input_image"}]}`, want: false},
		{name: "empty input array", body: `{"input":[]}`, want: false},
		{name: "numeric input", body: `{"input":123}`, want: false},
		{name: "unknown item", body: `{"input":[{"type":"custom","value":"hello"}]}`, want: false},
		{name: "invalid json", body: `{`, want: false},
		{name: "missing input", body: `{"model":"gpt-5.1"}`, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpenAIResponseReplayableWithoutPreviousID([]byte(tc.body)); got != tc.want {
				t.Fatalf("IsOpenAIResponseReplayableWithoutPreviousID()=%v want %v", got, tc.want)
			}
		})
	}
}
