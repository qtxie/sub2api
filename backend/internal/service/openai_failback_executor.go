package service

import (
	"sync"

	"github.com/alitto/pond/v2"
)

const (
	openAIFailbackProbeWorkerCount = 8
	openAIFailbackProbeQueueSize   = 64
)

type openAIFailbackProbeExecutor interface {
	Submit(task func()) bool
	Stop()
}

type pondOpenAIFailbackProbeExecutor struct {
	pool     pond.Pool
	stopOnce sync.Once
}

func newOpenAIFailbackProbeExecutor() openAIFailbackProbeExecutor {
	return &pondOpenAIFailbackProbeExecutor{
		pool: pond.NewPool(
			openAIFailbackProbeWorkerCount,
			pond.WithQueueSize(openAIFailbackProbeQueueSize),
		),
	}
}

func (e *pondOpenAIFailbackProbeExecutor) Submit(task func()) bool {
	if e == nil || e.pool == nil || task == nil || e.pool.Stopped() {
		return false
	}
	_, submitted := e.pool.TrySubmit(task)
	return submitted
}

func (e *pondOpenAIFailbackProbeExecutor) Stop() {
	if e == nil {
		return
	}
	e.stopOnce.Do(func() {
		if e.pool != nil {
			e.pool.StopAndWait()
		}
	})
}
