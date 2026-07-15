package service

import (
	"context"
	"errors"

	"github.com/gin-gonic/gin"
)

const openAIPreOutputMaxPreambleBytes = 1 << 20

var errOpenAIPreambleTooLarge = errors.New("openai pre-output preamble exceeded limit")

// openAIUpstreamContext preserves the bounded drain cancellation for ordinary
// Responses streams. Other paths keep the existing detached billing context.
func openAIUpstreamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if isOpenAIStreamDrainContext(ctx) {
		return ctx, func() {}
	}
	return detachUpstreamContext(ctx)
}

// The remaining OpenAIPreOutput helpers are compatibility hooks for stream
// writers shared with older paths. The request-wide retry/budget coordinator no
// longer exists, so these operations are stateless.
func OpenAIPreOutputFailureError(_ *gin.Context, _ context.Context, err error) error {
	return err
}

func OpenAIPreOutputWithWriterLock(_ *gin.Context, fn func() error) error {
	return fn()
}

func OpenAIPreOutputCommitSemantic(c *gin.Context, _ context.Context, fn func() error) (totalMs, attemptMs int, transitioned bool, err error) {
	err = fn()
	if err == nil {
		MarkOpenAIStreamDrainSemantic(c)
		transitioned = true
	}
	return
}

func OpenAIPreOutputSetHeaders(_ *gin.Context, fn func()) {
	fn()
}

func OpenAIPreOutputMarkClientDisconnected(c *gin.Context) {
	MarkOpenAIStreamDrainClientDisconnected(c)
}

func OpenAIPreOutputEnabled(_ *gin.Context) bool {
	return false
}

func OpenAIPreOutputClientConnected(c *gin.Context) bool {
	return OpenAIStreamDrainClientConnected(c)
}

func OpenAIPreOutputTransportStarted(_ *gin.Context) bool {
	return false
}

func OpenAIPreOutputSemanticStarted(c *gin.Context) bool {
	return OpenAIStreamDrainSemanticStarted(c)
}

func StopOpenAIPreOutputCommitted(_ *gin.Context) bool {
	return false
}

func appendOpenAIPreOutputPreamble(buf []byte, line string) ([]byte, error) {
	nextLen := len(buf) + len(line) + 1
	if nextLen > openAIPreOutputMaxPreambleBytes {
		return buf, errOpenAIPreambleTooLarge
	}
	buf = append(buf, line...)
	buf = append(buf, '\n')
	return buf, nil
}
