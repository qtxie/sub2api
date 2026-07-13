package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type writeDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *writeDeadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestOpsCaptureWriter_NilInnerWriter_NoPanic(t *testing.T) {
	w := &opsCaptureWriter{}
	w.ResponseWriter = nil

	assert.NotPanics(t, func() {
		assert.Equal(t, 0, w.Status())
	})
	assert.NotPanics(t, func() {
		assert.Equal(t, -1, w.Size())
	})
	assert.NotPanics(t, func() {
		assert.False(t, w.Written())
	})
	assert.NotPanics(t, func() {
		n, err := w.Write([]byte("test"))
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		n, err := w.WriteString("test")
		assert.Equal(t, 0, n)
		assert.NoError(t, err)
	})
	assert.NotPanics(t, func() {
		h := w.Header()
		assert.NotNil(t, h)
	})
	assert.NotPanics(t, func() {
		w.WriteHeader(200)
	})
	assert.NotPanics(t, func() {
		w.WriteHeaderNow()
	})
	assert.NotPanics(t, func() {
		w.Flush()
	})
	assert.NotPanics(t, func() {
		conn, rw, err := w.Hijack()
		assert.Nil(t, conn)
		assert.Nil(t, rw)
		assert.Error(t, err)
	})
	assert.NotPanics(t, func() {
		ch := w.CloseNotify()
		assert.NotNil(t, ch)
	})
	assert.NotPanics(t, func() {
		p := w.Pusher()
		assert.Nil(t, p)
	})
}

func TestOpsCaptureWriter_UnwrapsResponseControllerWriteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	underlying := &writeDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	c, _ := gin.CreateTestContext(underlying)
	w := &opsCaptureWriter{ResponseWriter: c.Writer}
	deadline := time.Now().Add(time.Second)

	require.NoError(t, http.NewResponseController(w).SetWriteDeadline(deadline))
	assert.Equal(t, deadline, underlying.deadline)
}
