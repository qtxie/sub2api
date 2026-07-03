package service

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	OpenAIPreviousResponseIDKindEmpty      = "empty"
	OpenAIPreviousResponseIDKindResponseID = "response_id"
	OpenAIPreviousResponseIDKindMessageID  = "message_id"
	OpenAIPreviousResponseIDKindUnknown    = "unknown"
)

var (
	openAIResponseIDPattern = regexp.MustCompile(`^resp_[A-Za-z0-9_-]{1,256}$`)
	openAIMessageIDPattern  = regexp.MustCompile(`^(msg|message|item|chatcmpl)_[A-Za-z0-9_-]{1,256}$`)
)

// ClassifyOpenAIPreviousResponseIDKind classifies previous_response_id to improve diagnostics.
func ClassifyOpenAIPreviousResponseIDKind(id string) string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return OpenAIPreviousResponseIDKindEmpty
	}
	if openAIResponseIDPattern.MatchString(trimmed) {
		return OpenAIPreviousResponseIDKindResponseID
	}
	if openAIMessageIDPattern.MatchString(strings.ToLower(trimmed)) {
		return OpenAIPreviousResponseIDKindMessageID
	}
	return OpenAIPreviousResponseIDKindUnknown
}

func IsOpenAIPreviousResponseIDLikelyMessageID(id string) bool {
	return ClassifyOpenAIPreviousResponseIDKind(id) == OpenAIPreviousResponseIDKindMessageID
}

func IsOpenAIResponseReplayableWithoutPreviousID(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	return openAIResponseInputReplayable(input)
}

func openAIResponseInputReplayable(input gjson.Result) bool {
	switch input.Type {
	case gjson.String:
		return strings.TrimSpace(input.String()) != ""
	case gjson.JSON:
		if input.IsArray() {
			for _, item := range input.Array() {
				if openAIResponseInputItemReplayable(item) {
					return true
				}
			}
			return false
		}
		return openAIResponseInputItemReplayable(input)
	default:
		return false
	}
}

func openAIResponseInputItemReplayable(item gjson.Result) bool {
	if !item.Exists() {
		return false
	}
	if item.Type == gjson.String {
		return strings.TrimSpace(item.String()) != ""
	}
	if item.Type != gjson.JSON {
		return false
	}
	itemType := strings.TrimSpace(item.Get("type").String())
	switch itemType {
	case "input_text":
		return strings.TrimSpace(item.Get("text").String()) != ""
	case "", "message":
		return openAIResponseMessageContentReplayable(item.Get("content"))
	case "input_image":
		return strings.TrimSpace(item.Get("image_url").String()) != "" ||
			strings.TrimSpace(item.Get("file_id").String()) != ""
	case "function_call_output":
		return false
	default:
		return false
	}
}

func openAIResponseMessageContentReplayable(content gjson.Result) bool {
	if !content.Exists() {
		return false
	}
	switch content.Type {
	case gjson.String:
		return strings.TrimSpace(content.String()) != ""
	case gjson.JSON:
		if content.IsArray() {
			for _, item := range content.Array() {
				if openAIResponseMessageContentItemReplayable(item) {
					return true
				}
			}
			return false
		}
		return openAIResponseMessageContentItemReplayable(content)
	default:
		return false
	}
}

func openAIResponseMessageContentItemReplayable(item gjson.Result) bool {
	if !item.Exists() {
		return false
	}
	if item.Type == gjson.String {
		return strings.TrimSpace(item.String()) != ""
	}
	if item.Type != gjson.JSON {
		return false
	}
	switch strings.TrimSpace(item.Get("type").String()) {
	case "input_text", "text":
		return strings.TrimSpace(item.Get("text").String()) != ""
	case "input_image":
		return strings.TrimSpace(item.Get("image_url").String()) != "" ||
			strings.TrimSpace(item.Get("file_id").String()) != ""
	default:
		return false
	}
}
