package service

import "testing"

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
		{name: "image input item", body: `{"input":[{"type":"input_image","image_url":"https://example.test/image.png"}]}`, want: true},
		{name: "function call output", body: `{"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`, want: false},
		{name: "reasoning encrypted only", body: `{"input":[{"type":"reasoning","encrypted_content":"gAAA"}]}`, want: false},
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
