package service

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const openAIRemoteCompactionV2Feature = "remote_compaction_v2"

// HasCompactionTriggerInInput detects an input item with
// type="compaction_trigger". The handler combines this body signal with the
// request path, stream flag, and Codex beta feature header to distinguish the
// native remote compaction v2 wire from the legacy /responses/compact bridge.
func HasCompactionTriggerInInput(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}

// IsOpenAIRemoteCompactionV2Request identifies the native HTTP Responses
// protocol. Unlike the legacy /responses/compact bridge, this stays on the
// streaming /responses endpoint and can legitimately take a long time before
// producing its compaction result.
func IsOpenAIRemoteCompactionV2Request(c *gin.Context, body []byte) bool {
	return gjson.GetBytes(body, "stream").Type == gjson.True &&
		HasCompactionTriggerInInput(body) &&
		hasOpenAICodexBetaFeature(c, openAIRemoteCompactionV2Feature)
}

// isOpenAIRemoteCompactionV2WebSocketTurn is the equivalent predicate for a
// response.create frame. WebSocket turns are inherently streamed, so they do
// not require the HTTP stream:true signal.
func isOpenAIRemoteCompactionV2WebSocketTurn(c *gin.Context, payload []byte) bool {
	return strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create" &&
		HasCompactionTriggerInInput(payload) &&
		hasOpenAICodexBetaFeature(c, openAIRemoteCompactionV2Feature)
}

func hasOpenAICodexBetaFeature(c *gin.Context, expected string) bool {
	if c == nil || c.Request == nil {
		return false
	}
	for _, header := range c.Request.Header.Values("x-codex-beta-features") {
		for _, feature := range strings.Split(header, ",") {
			if strings.TrimSpace(feature) == expected {
				return true
			}
		}
	}
	return false
}
