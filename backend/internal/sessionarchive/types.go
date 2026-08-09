package sessionarchive

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrRequestTooLarge     = errors.New("session archive request exceeds configured limit")
	ErrPartTooLarge        = errors.New("session archive part exceeds configured limit")
	ErrRemoteDownloadOff   = errors.New("remote media download is disabled")
	ErrUnsafeRemoteURL     = errors.New("remote media URL is not safe")
	ErrOpaqueFileReference = errors.New("opaque file reference cannot be archived as binary")
	ErrSessionNotFound     = errors.New("session archive not found")
	ErrBlobNotFound        = errors.New("session archive blob not found")
)

type Part struct {
	Kind             string
	Data             []byte
	SourcePath       string
	OriginalFilename string
	DeclaredMIME     string
	DetectedMIME     string
	SHA256           string
	ObjectKey        string
}

type Turn struct {
	Role  string
	Parts []Part
	Hash  string
}

type Capture struct {
	UserID              int64
	APIKeyID            int64
	GroupID             *int64
	IdentityKind        string
	ExternalSessionHash string
	Protocol            string
	Model               string
	RequestID           string
	PreviousResponseID  string
	Turns               []Turn
}

type CaptureResult struct {
	SessionID        int64
	RequestRowID     int64
	BranchKey        string
	ReusedTurnCount  int
	NewTurnCount     int
	IdempotentReplay bool
}

type SessionSummary struct {
	ID           int64     `json:"id"`
	UserID       int64     `json:"user_id"`
	APIKeyID     *int64    `json:"api_key_id,omitempty"`
	GroupID      *int64    `json:"group_id,omitempty"`
	IdentityKind string    `json:"identity_kind"`
	Protocol     string    `json:"protocol"`
	Model        string    `json:"model"`
	TurnCount    int       `json:"turn_count"`
	RequestCount int       `json:"request_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type StoredPart struct {
	ID               int64   `json:"id"`
	Ordinal          int     `json:"ordinal"`
	Kind             string  `json:"kind"`
	BlobID           int64   `json:"blob_id"`
	ByteLength       int64   `json:"byte_length"`
	DetectedMIME     string  `json:"detected_mime"`
	DeclaredMIME     string  `json:"declared_mime"`
	OriginalFilename string  `json:"original_filename"`
	SourcePath       string  `json:"source_path"`
	Text             *string `json:"text,omitempty"`
}

type StoredTurn struct {
	ID        int64        `json:"id"`
	Ordinal   int          `json:"ordinal"`
	Role      string       `json:"role"`
	BranchKey string       `json:"branch_key"`
	Parts     []StoredPart `json:"parts"`
}

type SessionDetail struct {
	SessionSummary
	Turns []StoredTurn `json:"turns"`
}

type BlobStorage interface {
	Put(ctx context.Context, key, contentType string, data []byte) error
	Open(ctx context.Context, key string) (io.ReadCloser, int64, string, error)
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

type pendingBlobDeletion struct {
	ID        int64
	ObjectKey string
}
