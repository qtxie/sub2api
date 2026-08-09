package sessionarchive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	coreservice "github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

type Service struct {
	repo      archiveRepository
	cfg       config.SessionArchiveConfig
	extractor *Extractor
	storage   BlobStorage
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

type archiveRepository interface {
	HasRequest(context.Context, int64, string) (CaptureResult, bool, error)
	BlobExists(context.Context, int64, string, string, int64) (bool, error)
	Persist(context.Context, Capture, bool) (CaptureResult, error)
	ListSessions(context.Context, int64, int, int) ([]SessionSummary, int64, error)
	GetSession(context.Context, int64) (*SessionDetail, error)
	GetBlob(context.Context, int64, int64) (blobRecord, error)
	DeleteSession(context.Context, int64) (int64, []pendingBlobDeletion, error)
	FinalizeBlobDeletion(context.Context, int64) error
	PendingBlobDeletions(context.Context, int) ([]pendingBlobDeletion, error)
	ExpiredSessionIDs(context.Context, time.Time, int) ([]int64, error)
}

func NewService(db *sql.DB, cfg *config.Config) (*Service, error) {
	archiveCfg := config.SessionArchiveConfig{Storage: "database", FailurePolicy: "strict", MaxRequestBytes: 256 << 20, MaxPartBytes: 64 << 20}
	if cfg != nil {
		archiveCfg = cfg.SessionArchive
	}
	if strings.TrimSpace(archiveCfg.Storage) == "" {
		archiveCfg.Storage = "database"
	}
	if strings.TrimSpace(archiveCfg.FailurePolicy) == "" {
		archiveCfg.FailurePolicy = "strict"
	}
	if archiveCfg.MaxRequestBytes <= 0 {
		archiveCfg.MaxRequestBytes = 256 << 20
	}
	if archiveCfg.MaxPartBytes <= 0 {
		archiveCfg.MaxPartBytes = 64 << 20
	}
	var storage BlobStorage
	var err error
	if archiveCfg.UsesS3() {
		storage, err = NewS3BlobStorage(context.Background(), archiveCfg)
		if err != nil {
			return nil, err
		}
	}
	var fetcher RemoteFetcher
	if archiveCfg.AllowRemoteDownload {
		fetcher = NewSafeRemoteFetcher(archiveCfg.AllowInsecureHTTP)
	}
	s := &Service{repo: NewRepository(db), cfg: archiveCfg, extractor: &Extractor{MaxPartBytes: archiveCfg.MaxPartBytes, Fetcher: fetcher}, storage: storage}
	if archiveCfg.RetentionDays > 0 || archiveCfg.UsesS3() {
		workerCtx, cancel := context.WithCancel(context.Background())
		s.cancel, s.done = cancel, make(chan struct{})
		go s.runMaintenance(workerCtx)
	}
	return s, nil
}

func (s *Service) Strict() bool { return s != nil && s.cfg.Strict() }

func (s *Service) Capture(ctx context.Context, capture Capture) (CaptureResult, error) {
	if s == nil || s.repo == nil || capture.UserID <= 0 {
		return CaptureResult{}, nil
	}
	if replay, ok, err := s.repo.HasRequest(ctx, capture.UserID, capture.RequestID); err != nil {
		return CaptureResult{}, err
	} else if ok {
		return replay, nil
	}
	uploadedKeys := make(map[string]struct{})
	for ti := range capture.Turns {
		turn := &capture.Turns[ti]
		for pi := range turn.Parts {
			part := &turn.Parts[pi]
			if part.SHA256 == "" {
				part.SHA256 = hashBytes(part.Data)
			}
			if s.cfg.UsesS3() && part.Kind != "text" {
				key := s.objectKey(capture.UserID, *part)
				exists, err := s.repo.BlobExists(ctx, capture.UserID, part.Kind, part.SHA256, int64(len(part.Data)))
				if err != nil {
					return CaptureResult{}, err
				}
				_, uploaded := uploadedKeys[key]
				if !exists && !uploaded {
					if err := s.storage.Put(ctx, key, part.DetectedMIME, part.Data); err != nil {
						return CaptureResult{}, err
					}
					uploadedKeys[key] = struct{}{}
				}
				part.ObjectKey = key
			}
		}
		turn.Hash = hashTurn(*turn)
	}
	return s.repo.Persist(ctx, capture, !s.cfg.UsesS3())
}

func (s *Service) objectKey(userID int64, part Part) string {
	prefix := strings.Trim(strings.TrimSpace(s.cfg.Prefix), "/")
	key := path.Join("users", strconv.FormatInt(userID, 10), part.Kind, part.SHA256[:2], part.SHA256)
	if prefix != "" {
		key = path.Join(prefix, key)
	}
	return key
}

func (s *Service) captureFromGin(c *gin.Context, apiKey *coreservice.APIKey, body []byte) error {
	baseRequestID := requestID(c)
	return s.captureBody(c, apiKey, body, baseRequestID, baseRequestID)
}

// CaptureFrame archives one Responses WebSocket response.create frame. The
// connection request ID is the session fallback, while frameID makes every turn
// independently idempotent.
func (s *Service) CaptureFrame(c *gin.Context, apiKey *coreservice.APIKey, body []byte, frameID string) error {
	if s == nil || c == nil || apiKey == nil || apiKey.User == nil || !apiKey.User.SessionStorageEnabled {
		return nil
	}
	baseRequestID := requestID(c)
	frameRequestID := normalizeRequestID(baseRequestID + ":" + strings.TrimSpace(frameID))
	err := s.captureBody(c, apiKey, body, frameRequestID, baseRequestID)
	if err != nil && !s.Strict() {
		return nil
	}
	return err
}

func (s *Service) captureBody(c *gin.Context, apiKey *coreservice.APIKey, body []byte, captureRequestID, fallbackIdentity string) error {
	turns, err := s.extractor.Extract(c.Request.Context(), body, c.GetHeader("Content-Type"))
	if err != nil {
		return err
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	previousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	identityKind := "request"
	identityValue := fallbackIdentity
	if explicit := coreservice.ExtractClientSessionID(c); explicit != "" {
		identityKind, identityValue = "explicit", explicit
	} else if conversationID := firstJSONValue(body, "conversation_id", "conversationId"); conversationID != "" {
		identityKind, identityValue = "conversation", conversationID
	} else if promptCacheKey := firstJSONValue(body, "prompt_cache_key", "promptCacheKey"); promptCacheKey != "" {
		identityKind, identityValue = "prompt_cache_key", promptCacheKey
	}
	protocol := protocolForPath(c.Request.URL.Path)
	var groupID *int64
	if apiKey.GroupID != nil {
		value := *apiKey.GroupID
		groupID = &value
	}
	_, err = s.Capture(c.Request.Context(), Capture{
		UserID: apiKey.User.ID, APIKeyID: apiKey.ID, GroupID: groupID,
		IdentityKind: identityKind, ExternalSessionHash: hashSessionIdentity(apiKey.User.ID, identityKind, identityValue),
		Protocol: protocol, Model: model, RequestID: captureRequestID, PreviousResponseID: previousResponseID, Turns: turns,
	})
	return err
}

func (s *Service) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || c.Request == nil || c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Body == nil {
			c.Next()
			return
		}
		apiKey, ok := servermiddleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.User == nil || !apiKey.User.SessionStorageEnabled {
			c.Next()
			return
		}
		limit := s.cfg.MaxRequestBytes
		if limit <= 0 {
			limit = 256 << 20
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
		_ = c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		if err == nil && int64(len(body)) > limit {
			err = ErrRequestTooLarge
		}
		if err == nil && len(body) > 0 {
			err = s.captureFromGin(c, apiKey, body)
		}
		if err != nil {
			if s.Strict() {
				c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"type": "session_archive_error", "code": "SESSION_ARCHIVE_FAILED", "message": "User session could not be saved"}})
				return
			}
			// Best effort intentionally allows the gateway request to continue.
		}
		c.Next()
	}
}

func requestID(c *gin.Context) string {
	for _, value := range []string{c.GetHeader("Idempotency-Key"), c.GetHeader("X-Request-ID")} {
		if normalized := normalizeRequestID(value); normalized != "" {
			return normalized
		}
	}
	if c != nil && c.Request != nil {
		if value, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(value) != "" {
			return normalizeRequestID(value)
		}
	}
	return uuid.NewString()
}

func normalizeRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if len(value) > 128 {
		return "sha256:" + hashBytes([]byte(value))
	}
	return value
}

func firstJSONValue(body []byte, paths ...string) string {
	for _, p := range paths {
		if v := strings.TrimSpace(gjson.GetBytes(body, p).String()); v != "" {
			return v
		}
	}
	return ""
}

func protocolForPath(requestPath string) string {
	switch {
	case strings.Contains(requestPath, "chat/completions"):
		return "openai_chat"
	case strings.Contains(requestPath, "responses"):
		return "openai_responses"
	case strings.Contains(requestPath, "messages"):
		return "anthropic_messages"
	case strings.Contains(requestPath, "/v1beta/"):
		return "gemini"
	case strings.Contains(requestPath, "images"):
		return "openai_images"
	default:
		return "gateway"
	}
}

func (s *Service) ListSessions(ctx context.Context, userID int64, limit, offset int) ([]SessionSummary, int64, error) {
	return s.repo.ListSessions(ctx, userID, limit, offset)
}
func (s *Service) GetSession(ctx context.Context, id int64) (*SessionDetail, error) {
	return s.repo.GetSession(ctx, id)
}

func (s *Service) OpenBlob(ctx context.Context, sessionID, blobID int64) (io.ReadCloser, int64, string, string, error) {
	record, err := s.repo.GetBlob(ctx, sessionID, blobID)
	if err != nil {
		return nil, 0, "", "", err
	}
	if record.ObjectKey != "" {
		if s.storage == nil {
			return nil, 0, "", "", errors.New("session archive object storage is not configured")
		}
		reader, length, mimeType, err := s.storage.Open(ctx, record.ObjectKey)
		return reader, length, mimeType, record.Filename, err
	}
	return io.NopCloser(bytes.NewReader(record.Data)), int64(len(record.Data)), record.MIME, record.Filename, nil
}

func (s *Service) DeleteSession(ctx context.Context, id int64) error {
	_, _, err := s.repo.DeleteSession(ctx, id)
	if err != nil {
		return err
	}
	return nil
}

func (s *Service) runMaintenance(ctx context.Context) {
	defer close(s.done)
	_ = s.CleanupExpired(ctx)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.CleanupExpired(ctx)
		}
	}
}

func (s *Service) CleanupExpired(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return nil
	}
	if s.cfg.RetentionDays > 0 {
		before := time.Now().Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour)
		for {
			ids, err := s.repo.ExpiredSessionIDs(ctx, before, 100)
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			for _, id := range ids {
				if err := s.DeleteSession(ctx, id); err != nil {
					return err
				}
			}
		}
	}
	if s.storage != nil {
		pending, err := s.repo.PendingBlobDeletions(ctx, 100)
		if err != nil {
			return err
		}
		for _, item := range pending {
			if err := s.storage.Delete(ctx, item.ObjectKey); err != nil {
				continue
			}
			if err := s.repo.FinalizeBlobDeletion(ctx, item.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	if s.done == nil {
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
