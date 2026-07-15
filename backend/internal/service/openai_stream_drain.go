package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

const openAIStreamDrainKey = "openai_stream_drain"

var errOpenAIStreamDrainExpired = errors.New("openai stream drain expired after client disconnect")

// OpenAIStreamDrainSettings bounds upstream usage collection after the client
// disconnects. The shorter pre-output window applies until semantic output is
// committed; the post-output window applies afterwards.
type OpenAIStreamDrainSettings struct {
	PreDisconnectDrain  time.Duration
	PostDisconnectDrain time.Duration
}

type openAIStreamDrain struct {
	workCtx context.Context
	cancel  context.CancelCauseFunc

	settings  OpenAIStreamDrainSettings
	semantic  atomic.Bool
	connected atomic.Bool

	disconnect     chan struct{}
	disconnectOnce sync.Once
	stop           chan struct{}
	stopOnce       sync.Once
	done           chan struct{}
}

type openAIStreamDrainContextKey struct{}

// StartOpenAIStreamDrain creates a detached upstream context whose lifetime is
// still bounded after client disconnect. Call the returned function when the
// request finishes normally.
func StartOpenAIStreamDrain(c *gin.Context, clientCtx context.Context, settings OpenAIStreamDrainSettings) func() {
	if c == nil || clientCtx == nil {
		return func() {}
	}
	base := context.WithValue(context.WithoutCancel(clientCtx), openAIStreamDrainContextKey{}, true)
	workCtx, cancel := context.WithCancelCause(base)
	drain := &openAIStreamDrain{
		workCtx:    workCtx,
		cancel:     cancel,
		settings:   settings,
		disconnect: make(chan struct{}),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	drain.connected.Store(clientCtx.Err() == nil)
	c.Set(openAIStreamDrainKey, drain)
	go drain.watch(clientCtx)

	return func() {
		drain.stopOnce.Do(func() { close(drain.stop) })
		drain.cancel(context.Canceled)
		<-drain.done
	}
}

func (d *openAIStreamDrain) watch(clientCtx context.Context) {
	defer close(d.done)
	select {
	case <-clientCtx.Done():
		d.markDisconnected()
	case <-d.disconnect:
	case <-d.stop:
		return
	}

	drainFor := d.disconnectDrainDuration()
	if drainFor <= 0 {
		d.cancel(errOpenAIStreamDrainExpired)
		return
	}
	timer := time.NewTimer(drainFor)
	defer timer.Stop()
	select {
	case <-timer.C:
		d.cancel(errOpenAIStreamDrainExpired)
	case <-d.stop:
	}
}

func (d *openAIStreamDrain) disconnectDrainDuration() time.Duration {
	if d != nil && d.semantic.Load() {
		return d.settings.PostDisconnectDrain
	}
	if d == nil {
		return 0
	}
	return d.settings.PreDisconnectDrain
}

func (d *openAIStreamDrain) markDisconnected() {
	if d == nil {
		return
	}
	d.connected.Store(false)
	d.disconnectOnce.Do(func() { close(d.disconnect) })
}

func getOpenAIStreamDrain(c *gin.Context) *openAIStreamDrain {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAIStreamDrainKey)
	if !ok {
		return nil
	}
	drain, _ := value.(*openAIStreamDrain)
	return drain
}

// OpenAIStreamDrainContext returns the bounded detached context for the active
// stream, or parent when drain control is not enabled.
func OpenAIStreamDrainContext(c *gin.Context, parent context.Context) context.Context {
	if drain := getOpenAIStreamDrain(c); drain != nil {
		return drain.workCtx
	}
	return parent
}

func MarkOpenAIStreamDrainSemantic(c *gin.Context) {
	if drain := getOpenAIStreamDrain(c); drain != nil {
		drain.semantic.Store(true)
	}
}

func MarkOpenAIStreamDrainClientDisconnected(c *gin.Context) {
	if drain := getOpenAIStreamDrain(c); drain != nil {
		drain.markDisconnected()
	}
}

func OpenAIStreamDrainClientConnected(c *gin.Context) bool {
	if drain := getOpenAIStreamDrain(c); drain != nil {
		return drain.connected.Load()
	}
	return c == nil || c.Request == nil || c.Request.Context().Err() == nil
}

func OpenAIStreamDrainSemanticStarted(c *gin.Context) bool {
	if drain := getOpenAIStreamDrain(c); drain != nil {
		return drain.semantic.Load()
	}
	return false
}

func isOpenAIStreamDrainContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(openAIStreamDrainContextKey{}).(bool)
	return marked
}
