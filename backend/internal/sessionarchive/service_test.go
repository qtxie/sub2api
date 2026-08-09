package sessionarchive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	coreservice "github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeArchiveRepository struct {
	persisted       []Capture
	persistErr      error
	blobs           map[string]bool
	deleteResult    []pendingBlobDeletion
	deletedSessions []int64
	expiredBatches  [][]int64
	expiredCalls    int
	pending         []pendingBlobDeletion
	finalized       []int64
	blob            *blobRecord
}

func (f *fakeArchiveRepository) HasRequest(context.Context, int64, string) (CaptureResult, bool, error) {
	return CaptureResult{}, false, nil
}
func (f *fakeArchiveRepository) BlobExists(_ context.Context, _ int64, kind, sha string, length int64) (bool, error) {
	return f.blobs[kind+sha], nil
}
func (f *fakeArchiveRepository) Persist(_ context.Context, c Capture, _ bool) (CaptureResult, error) {
	if f.persistErr != nil {
		return CaptureResult{}, f.persistErr
	}
	f.persisted = append(f.persisted, c)
	return CaptureResult{SessionID: 1}, nil
}
func (*fakeArchiveRepository) ListSessions(context.Context, int64, int, int) ([]SessionSummary, int64, error) {
	return nil, 0, nil
}
func (*fakeArchiveRepository) GetSession(context.Context, int64) (*SessionDetail, error) {
	return nil, ErrSessionNotFound
}
func (f *fakeArchiveRepository) GetBlob(context.Context, int64, int64) (blobRecord, error) {
	if f.blob != nil {
		return *f.blob, nil
	}
	return blobRecord{}, ErrBlobNotFound
}
func (f *fakeArchiveRepository) DeleteSession(_ context.Context, id int64) (int64, []pendingBlobDeletion, error) {
	f.deletedSessions = append(f.deletedSessions, id)
	return 1, f.deleteResult, nil
}
func (f *fakeArchiveRepository) FinalizeBlobDeletion(_ context.Context, id int64) error {
	f.finalized = append(f.finalized, id)
	return nil
}
func (f *fakeArchiveRepository) PendingBlobDeletions(context.Context, int) ([]pendingBlobDeletion, error) {
	return f.pending, nil
}
func (f *fakeArchiveRepository) ExpiredSessionIDs(context.Context, time.Time, int) ([]int64, error) {
	if f.expiredCalls >= len(f.expiredBatches) {
		return nil, nil
	}
	out := f.expiredBatches[f.expiredCalls]
	f.expiredCalls++
	return out, nil
}

type fakeBlobStorage struct{ puts, deletes int }

func (f *fakeBlobStorage) Put(context.Context, string, string, []byte) error { f.puts++; return nil }
func (*fakeBlobStorage) Open(context.Context, string) (io.ReadCloser, int64, string, error) {
	return nil, 0, "", errors.New("unused")
}
func (f *fakeBlobStorage) Delete(context.Context, string) error       { f.deletes++; return nil }
func (*fakeBlobStorage) Exists(context.Context, string) (bool, error) { return false, nil }

func TestCaptureUploadsRepeatedBinaryOnlyOnceButKeepsOccurrences(t *testing.T) {
	repo := &fakeArchiveRepository{}
	storage := &fakeBlobStorage{}
	s := &Service{repo: repo, storage: storage, cfg: config.SessionArchiveConfig{Storage: "s3", Prefix: "archive"}}
	data := []byte("same image")
	part := Part{Kind: "image", Data: data, DetectedMIME: "image/png", SHA256: hashBytes(data)}
	_, err := s.Capture(context.Background(), Capture{UserID: 7, RequestID: "req-1", Turns: []Turn{{Role: "user", Parts: []Part{part, part}}}})
	require.NoError(t, err)
	require.Equal(t, 1, storage.puts)
	require.Len(t, repo.persisted, 1)
	require.Len(t, repo.persisted[0].Turns[0].Parts, 2)
	require.Equal(t, repo.persisted[0].Turns[0].Parts[0].ObjectKey, repo.persisted[0].Turns[0].Parts[1].ObjectKey)
}

func TestMiddlewareHonorsPerUserFlagAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name        string
		enabled     bool
		wantPersist int
	}{{"disabled", false, 0}, {"enabled", true, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeArchiveRepository{}
			s := &Service{repo: repo, cfg: config.SessionArchiveConfig{Storage: "database", FailurePolicy: "strict", MaxRequestBytes: 1024}, extractor: &Extractor{MaxPartBytes: 1024}}
			r := gin.New()
			r.POST("/v1/chat/completions", func(c *gin.Context) {
				c.Set(string(servermiddleware.ContextKeyAPIKey), &coreservice.APIKey{ID: 9, User: &coreservice.User{ID: 7, SessionStorageEnabled: tt.enabled}})
				c.Next()
			}, s.Middleware(), func(c *gin.Context) { body, _ := io.ReadAll(c.Request.Body); c.String(200, string(body)) })
			body := `{"model":"gpt","messages":[{"role":"user","content":"hello"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "req-1")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 200, w.Code)
			require.Equal(t, body, w.Body.String())
			require.Len(t, repo.persisted, tt.wantPersist)
		})
	}
}

func TestMiddlewareStrictFailureBlocksAndBestEffortContinues(t *testing.T) {
	for _, tt := range []struct {
		policy string
		status int
	}{{"strict", 503}, {"best_effort", 204}} {
		repo := &fakeArchiveRepository{persistErr: errors.New("database unavailable")}
		s := &Service{repo: repo, cfg: config.SessionArchiveConfig{Storage: "database", FailurePolicy: tt.policy, MaxRequestBytes: 1024}, extractor: &Extractor{MaxPartBytes: 1024}}
		r := gin.New()
		r.POST("/v1/messages", func(c *gin.Context) {
			c.Set(string(servermiddleware.ContextKeyAPIKey), &coreservice.APIKey{ID: 1, User: &coreservice.User{ID: 2, SessionStorageEnabled: true}})
			c.Next()
		}, s.Middleware(), func(c *gin.Context) { c.Status(204) })
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "req")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, tt.status, w.Code)
	}
}

func TestDeleteDefersS3PhysicalRemovalAndMaintenanceFinalizes(t *testing.T) {
	repo := &fakeArchiveRepository{
		deleteResult: []pendingBlobDeletion{{ID: 8, ObjectKey: "archive/object"}},
		pending:      []pendingBlobDeletion{{ID: 8, ObjectKey: "archive/object"}},
	}
	storage := &fakeBlobStorage{}
	s := &Service{repo: repo, storage: storage, cfg: config.SessionArchiveConfig{Storage: "s3"}}
	require.NoError(t, s.DeleteSession(context.Background(), 3))
	require.Zero(t, storage.deletes)
	require.NoError(t, s.CleanupExpired(context.Background()))
	require.Equal(t, 1, storage.deletes)
	require.Equal(t, []int64{8}, repo.finalized)
}

func TestRetentionCleanupDeletesExpiredSessionsInBatches(t *testing.T) {
	repo := &fakeArchiveRepository{expiredBatches: [][]int64{{4, 5}}}
	s := &Service{repo: repo, cfg: config.SessionArchiveConfig{Storage: "database", RetentionDays: 30}}
	require.NoError(t, s.CleanupExpired(context.Background()))
	require.Equal(t, []int64{4, 5}, repo.deletedSessions)
}

func TestOpenBlobRequiresConfiguredObjectStorage(t *testing.T) {
	repo := &fakeArchiveRepository{blob: &blobRecord{ObjectKey: "archive/object"}}
	s := &Service{repo: repo}
	reader, _, _, _, err := s.OpenBlob(context.Background(), 1, 2)
	require.Nil(t, reader)
	require.ErrorContains(t, err, "object storage is not configured")
}
