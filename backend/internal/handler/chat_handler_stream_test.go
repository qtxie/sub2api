package handler

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestExtractGatewayStreamDeltaSupportsProviderShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "chat completions",
			body: `{"choices":[{"delta":{"content":"hello"}}]}`,
			want: "hello",
		},
		{
			name: "chat completions output text",
			body: `{"choices":[{"delta":{"output_text":"output"}}]}`,
			want: "output",
		},
		{
			name: "chat completions reasoning content ignored",
			body: `{"choices":[{"delta":{"reasoning_content":"reasoning"}}]}`,
			want: "",
		},
		{
			name: "responses reasoning delta ignored",
			body: `{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
			want: "",
		},
		{
			name: "responses",
			body: `{"type":"response.output_text.delta","delta":"world"}`,
			want: "world",
		},
		{
			name: "anthropic",
			body: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"anthropic"}}`,
			want: "anthropic",
		},
		{
			name: "gemini",
			body: `{"candidates":[{"content":{"parts":[{"text":"gem"},{"text":"ini"}]}}]}`,
			want: "gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractGatewayStreamDelta([]byte(tt.body), false)
			if err != nil {
				t.Fatalf("extractGatewayStreamDelta() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("extractGatewayStreamDelta() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestForwardGatewayChatStreamFlushesEachSSEEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	handler := &ChatHandler{}

	content, err := handler.forwardGatewayChatStream(ctx, strings.NewReader(
		"event: message\n"+
			"data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"+
			"event: message\n"+
			"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"+
			"data: [DONE]\n\n",
	))
	if err != nil {
		t.Fatalf("forwardGatewayChatStream() error = %v", err)
	}
	if content != "hello world" {
		t.Fatalf("forwardGatewayChatStream() content = %q, want hello world", content)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"content":"hello "`) || !strings.Contains(body, `"content":"world"`) {
		t.Fatalf("forwarded body missing progressive deltas: %s", body)
	}
	if strings.Index(body, `"content":"hello "`) > strings.Index(body, `"content":"world"`) {
		t.Fatalf("forwarded deltas are out of order: %s", body)
	}
}

func TestExtractGatewayStreamDeltaSkipsFinalAggregateAfterDeltas(t *testing.T) {
	got, err := extractGatewayStreamDelta([]byte(`{"choices":[{"message":{"content":"complete"}}]}`), true)
	if err != nil {
		t.Fatalf("extractGatewayStreamDelta() error = %v", err)
	}
	if got != "" {
		t.Fatalf("extractGatewayStreamDelta() = %q, want empty", got)
	}
}

func TestExtractGatewayStreamDeltaReturnsStreamError(t *testing.T) {
	_, err := extractGatewayStreamDelta([]byte(`{"type":"response.failed","response":{"error":{"message":"upstream failed"}}}`), false)
	if err == nil || err.Error() != "upstream failed" {
		t.Fatalf("extractGatewayStreamDelta() error = %v, want upstream failed", err)
	}
}

func TestBuildGatewayChatRequestAddsTransientAttachmentsToLatestUserMessage(t *testing.T) {
	handler := &ChatHandler{}
	conversation := &service.ChatConversation{
		Model:        "gpt-5.4",
		SystemPrompt: "system",
		Messages: []service.ChatMessage{
			{Role: service.ChatRoleUser, Content: "first"},
			{Role: service.ChatRoleAssistant, Content: "reply"},
			{Role: service.ChatRoleUser, Content: "describe this"},
		},
	}
	attachments, err := validateChatStreamAttachments([]chatStreamAttachmentRequest{
		{
			Type:     "image",
			Name:     "image.png",
			MIMEType: "image/png",
			DataURL:  "data:image/png;base64,QUJD",
		},
		{
			Type:     "file",
			Name:     "notes.txt",
			MIMEType: "text/plain",
			Text:     "temporary context",
		},
	})
	if err != nil {
		t.Fatalf("validateChatStreamAttachments() error = %v", err)
	}

	body, err := handler.buildGatewayChatRequest(conversation, attachments)
	if err != nil {
		t.Fatalf("buildGatewayChatRequest() error = %v", err)
	}
	messages, ok := body["messages"].([]gatewayChatMessage)
	if !ok {
		t.Fatalf("messages type = %T, want []gatewayChatMessage", body["messages"])
	}
	if got, ok := messages[1].Content.(string); !ok || got != "first" {
		t.Fatalf("first user content = %#v, want persisted string", messages[1].Content)
	}
	parts, ok := messages[3].Content.([]map[string]any)
	if !ok {
		t.Fatalf("latest user content = %T, want multimodal parts", messages[3].Content)
	}
	if len(parts) != 3 {
		t.Fatalf("parts len = %d, want 3", len(parts))
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "describe this" {
		t.Fatalf("text part = %#v", parts[0])
	}
	imageURL := parts[1]["image_url"].(map[string]string)["url"]
	if imageURL != "data:image/png;base64,QUJD" {
		t.Fatalf("image url = %q", imageURL)
	}
	if parts[2]["type"] != "text" || !strings.Contains(parts[2]["text"].(string), "temporary context") {
		t.Fatalf("file part = %#v", parts[2])
	}
}

func TestValidateChatStreamAttachmentsRejectsUnsupportedFile(t *testing.T) {
	_, err := validateChatStreamAttachments([]chatStreamAttachmentRequest{{
		Type:     "file",
		Name:     "report.pdf",
		MIMEType: "application/pdf",
		Text:     "pretend pdf",
	}})
	if err == nil || err.Error() != "unsupported file attachment" {
		t.Fatalf("validateChatStreamAttachments() error = %v, want unsupported file attachment", err)
	}
}

func TestValidateChatStreamAttachmentsRejectsInvalidImageDataURL(t *testing.T) {
	_, err := validateChatStreamAttachments([]chatStreamAttachmentRequest{{
		Type:     "image",
		Name:     "image.png",
		MIMEType: "image/png",
		DataURL:  "data:image/png;base64,not base64!",
	}})
	if err == nil || err.Error() != "invalid image attachment" {
		t.Fatalf("validateChatStreamAttachments() error = %v, want invalid image attachment", err)
	}
}

func TestFilterChatImageGenerationModels(t *testing.T) {
	models := []string{
		"gpt-5.4",
		"gpt-image-2",
		"gemini-2.5-pro",
		"models/gemini-3.1-flash-image-preview",
		"grok-imagine-image-quality",
		"imagen-4.0-generate",
		"dall-e-3",
		"flux-1.1-pro",
	}
	want := []string{"gpt-5.4", "gemini-2.5-pro"}
	if got := filterChatImageGenerationModels(models); !reflect.DeepEqual(got, want) {
		t.Fatalf("filterChatImageGenerationModels() = %#v, want %#v", got, want)
	}
}

func TestIsChatImageGenerationModelDoesNotBlockImageInputModels(t *testing.T) {
	for _, model := range []string{"gpt-4o", "gpt-5.4", "claude-sonnet-4", "gemini-2.5-pro"} {
		if isChatImageGenerationModel(model) {
			t.Fatalf("isChatImageGenerationModel(%q) = true, want false", model)
		}
	}
}
