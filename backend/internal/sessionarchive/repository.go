package sessionarchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

type persistedTurn struct {
	ID                 int64
	Ordinal            int
	Role, Hash, Branch string
}

func (r *Repository) HasRequest(ctx context.Context, userID int64, requestID string) (CaptureResult, bool, error) {
	var out CaptureResult
	err := r.db.QueryRowContext(ctx, `
		SELECT session_id, id, branch_key, reused_turn_count, new_turn_count
		FROM user_session_requests WHERE user_id=$1 AND request_id=$2`, userID, requestID).
		Scan(&out.SessionID, &out.RequestRowID, &out.BranchKey, &out.ReusedTurnCount, &out.NewTurnCount)
	if errors.Is(err, sql.ErrNoRows) {
		return CaptureResult{}, false, nil
	}
	if err != nil {
		return CaptureResult{}, false, err
	}
	out.IdempotentReplay = true
	return out, true, nil
}

func (r *Repository) BlobExists(ctx context.Context, userID int64, kind, sha string, length int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM user_session_blobs WHERE user_id=$1 AND kind=$2 AND content_sha256=$3 AND byte_length=$4 AND storage_state='ready'
	)`, userID, kind, sha, length).Scan(&exists)
	return exists, err
}

func (r *Repository) Persist(ctx context.Context, capture Capture, databaseStorage bool) (CaptureResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CaptureResult{}, err
	}
	defer tx.Rollback()
	lockKey := fmt.Sprintf("sessionarchive:%d:%s", capture.UserID, capture.ExternalSessionHash)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, lockKey); err != nil {
		return CaptureResult{}, err
	}

	var replay CaptureResult
	err = tx.QueryRowContext(ctx, `SELECT session_id,id,branch_key,reused_turn_count,new_turn_count FROM user_session_requests WHERE user_id=$1 AND request_id=$2`, capture.UserID, capture.RequestID).
		Scan(&replay.SessionID, &replay.RequestRowID, &replay.BranchKey, &replay.ReusedTurnCount, &replay.NewTurnCount)
	if err == nil {
		replay.IdempotentReplay = true
		return replay, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CaptureResult{}, err
	}

	branchKey := uuid.NewString()
	var sessionID int64
	var activeBranch string
	var activeHead sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO user_sessions(user_id,api_key_id,group_id,identity_kind,external_session_hash,protocol,model,active_branch_key)
		VALUES($1,NULLIF($2,0),$3,$4,$5,$6,$7,$8)
		ON CONFLICT(user_id,identity_kind,external_session_hash) DO UPDATE SET
			api_key_id=EXCLUDED.api_key_id, group_id=EXCLUDED.group_id,
			protocol=EXCLUDED.protocol, model=EXCLUDED.model, updated_at=NOW()
		RETURNING id,active_branch_key,active_head_turn_id`,
		capture.UserID, capture.APIKeyID, capture.GroupID, capture.IdentityKind, capture.ExternalSessionHash, capture.Protocol, capture.Model, branchKey).
		Scan(&sessionID, &activeBranch, &activeHead)
	if err != nil {
		return CaptureResult{}, err
	}

	existing, err := loadTurnChain(ctx, tx, activeHead)
	if err != nil {
		return CaptureResult{}, err
	}
	lcp := longestCommonPrefix(existing, capture.Turns)
	if lcp == len(existing) {
		branchKey = activeBranch
	}
	if branchKey == "" {
		branchKey = uuid.NewString()
	}
	if lcp < len(existing) {
		branchKey = uuid.NewString()
	}

	var parentID sql.NullInt64
	if lcp > 0 {
		parentID = sql.NullInt64{Int64: existing[lcp-1].ID, Valid: true}
	}
	headID := parentID
	newCount := 0
	for i := lcp; i < len(capture.Turns); i++ {
		turn := capture.Turns[i]
		var turnID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO user_session_turns(session_id,branch_key,parent_turn_id,ordinal,role,content_hash,first_request_id)
			VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			sessionID, branchKey, nullableInt64(parentID), i, turn.Role, turn.Hash, capture.RequestID).Scan(&turnID)
		if err != nil {
			return CaptureResult{}, err
		}
		for ordinal, part := range turn.Parts {
			blobID, err := upsertBlob(ctx, tx, capture.UserID, part, databaseStorage)
			if err != nil {
				return CaptureResult{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO user_session_parts(turn_id,ordinal,kind,blob_id,source_path,original_filename,declared_mime)
				VALUES($1,$2,$3,$4,$5,$6,$7)`, turnID, ordinal, part.Kind, blobID, part.SourcePath, part.OriginalFilename, part.DeclaredMIME); err != nil {
				return CaptureResult{}, err
			}
		}
		parentID = sql.NullInt64{Int64: turnID, Valid: true}
		headID = parentID
		newCount++
	}
	if len(capture.Turns) == 0 {
		headID = sql.NullInt64{}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_sessions SET active_branch_key=$2,active_head_turn_id=$3,updated_at=NOW() WHERE id=$1`, sessionID, branchKey, nullableInt64(headID)); err != nil {
		return CaptureResult{}, err
	}
	var requestRowID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO user_session_requests(session_id,user_id,request_id,previous_response_id,branch_key,head_turn_id,incoming_sequence_hash,reused_turn_count,new_turn_count)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, sessionID, capture.UserID, capture.RequestID,
		capture.PreviousResponseID, branchKey, nullableInt64(headID), hashSequence(capture.Turns), lcp, newCount).Scan(&requestRowID)
	if err != nil {
		return CaptureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CaptureResult{}, err
	}
	return CaptureResult{SessionID: sessionID, RequestRowID: requestRowID, BranchKey: branchKey, ReusedTurnCount: lcp, NewTurnCount: newCount}, nil
}

func loadTurnChain(ctx context.Context, tx *sql.Tx, head sql.NullInt64) ([]persistedTurn, error) {
	if !head.Valid {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		WITH RECURSIVE chain AS (
			SELECT id,parent_turn_id,ordinal,role,content_hash,branch_key,0 AS depth FROM user_session_turns WHERE id=$1
			UNION ALL
			SELECT p.id,p.parent_turn_id,p.ordinal,p.role,p.content_hash,p.branch_key,c.depth+1
			FROM user_session_turns p JOIN chain c ON p.id=c.parent_turn_id
		)
		SELECT id,ordinal,role,content_hash,branch_key FROM chain ORDER BY depth DESC`, head.Int64)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistedTurn
	for rows.Next() {
		var t persistedTurn
		if err := rows.Scan(&t.ID, &t.Ordinal, &t.Role, &t.Hash, &t.Branch); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func longestCommonPrefix(existing []persistedTurn, incoming []Turn) int {
	limit := len(existing)
	if len(incoming) < limit {
		limit = len(incoming)
	}
	i := 0
	for i < limit && existing[i].Hash == incoming[i].Hash {
		i++
	}
	return i
}

func upsertBlob(ctx context.Context, tx *sql.Tx, userID int64, part Part, databaseStorage bool) (int64, error) {
	var inline any
	var object any
	if databaseStorage || part.Kind == "text" {
		inline = part.Data
	} else {
		object = part.ObjectKey
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO user_session_blobs(user_id,kind,content_sha256,byte_length,detected_mime,inline_bytes,object_key,storage_state)
		VALUES($1,$2,$3,$4,$5,$6,$7,'ready')
		ON CONFLICT(user_id,kind,content_sha256,byte_length) DO UPDATE SET
			detected_mime=CASE WHEN user_session_blobs.detected_mime='' THEN EXCLUDED.detected_mime ELSE user_session_blobs.detected_mime END,
			storage_state='ready', deletion_pending_at=NULL
		RETURNING id`, userID, part.Kind, part.SHA256, len(part.Data), part.DetectedMIME, inline, object).Scan(&id)
	return id, err
}

func nullableInt64(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}

func (r *Repository) ListSessions(ctx context.Context, userID int64, limit, offset int) ([]SessionSummary, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := ""
	args := []any{}
	if userID > 0 {
		where = " WHERE s.user_id=$1"
		args = append(args, userID)
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM user_sessions s"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	li, oi := len(args)-1, len(args)
	query := fmt.Sprintf(`SELECT s.id,s.user_id,s.api_key_id,s.group_id,s.identity_kind,s.protocol,s.model,
		(SELECT COUNT(*) FROM user_session_turns t WHERE t.session_id=s.id),
		(SELECT COUNT(*) FROM user_session_requests r WHERE r.session_id=s.id),s.created_at,s.updated_at
		FROM user_sessions s%s ORDER BY s.updated_at DESC,s.id DESC LIMIT $%d OFFSET $%d`, where, li, oi)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]SessionSummary, 0)
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.ID, &s.UserID, &s.APIKeyID, &s.GroupID, &s.IdentityKind, &s.Protocol, &s.Model, &s.TurnCount, &s.RequestCount, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

func (r *Repository) GetSession(ctx context.Context, id int64) (*SessionDetail, error) {
	var out SessionDetail
	err := r.db.QueryRowContext(ctx, `SELECT s.id,s.user_id,s.api_key_id,s.group_id,s.identity_kind,s.protocol,s.model,
		(SELECT COUNT(*) FROM user_session_turns t WHERE t.session_id=s.id),(SELECT COUNT(*) FROM user_session_requests r WHERE r.session_id=s.id),s.created_at,s.updated_at
		FROM user_sessions s WHERE s.id=$1`, id).Scan(&out.ID, &out.UserID, &out.APIKeyID, &out.GroupID, &out.IdentityKind, &out.Protocol, &out.Model, &out.TurnCount, &out.RequestCount, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `WITH RECURSIVE chain AS (
		SELECT t.id,t.parent_turn_id,t.ordinal,t.role,t.branch_key,0 depth FROM user_session_turns t JOIN user_sessions s ON s.active_head_turn_id=t.id WHERE s.id=$1
		UNION ALL SELECT p.id,p.parent_turn_id,p.ordinal,p.role,p.branch_key,c.depth+1 FROM user_session_turns p JOIN chain c ON p.id=c.parent_turn_id)
		SELECT id,ordinal,role,branch_key FROM chain ORDER BY depth DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t StoredTurn
		if err := rows.Scan(&t.ID, &t.Ordinal, &t.Role, &t.BranchKey); err != nil {
			return nil, err
		}
		parts, err := r.loadParts(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		t.Parts = parts
		out.Turns = append(out.Turns, t)
	}
	return &out, rows.Err()
}

func (r *Repository) loadParts(ctx context.Context, turnID int64) ([]StoredPart, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT p.id,p.ordinal,p.kind,p.blob_id,b.byte_length,b.detected_mime,p.declared_mime,p.original_filename,p.source_path,
		CASE WHEN p.kind='text' AND b.inline_bytes IS NOT NULL THEN b.inline_bytes ELSE NULL END
		FROM user_session_parts p JOIN user_session_blobs b ON b.id=p.blob_id WHERE p.turn_id=$1 ORDER BY p.ordinal`, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredPart
	for rows.Next() {
		var p StoredPart
		var textBytes []byte
		if err := rows.Scan(&p.ID, &p.Ordinal, &p.Kind, &p.BlobID, &p.ByteLength, &p.DetectedMIME, &p.DeclaredMIME, &p.OriginalFilename, &p.SourcePath, &textBytes); err != nil {
			return nil, err
		}
		if textBytes != nil {
			text := string(textBytes)
			p.Text = &text
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type blobRecord struct {
	UserID                    int64
	Data                      []byte
	ObjectKey, MIME, Filename string
	Length                    int64
}

func (r *Repository) GetBlob(ctx context.Context, sessionID, blobID int64) (blobRecord, error) {
	var b blobRecord
	err := r.db.QueryRowContext(ctx, `SELECT b.user_id,b.inline_bytes,COALESCE(b.object_key,''),b.detected_mime,b.byte_length,
		COALESCE(NULLIF(p.original_filename,''),'archive-'||b.id::text)
		FROM user_session_blobs b JOIN user_session_parts p ON p.blob_id=b.id JOIN user_session_turns t ON t.id=p.turn_id
		WHERE t.session_id=$1 AND b.id=$2 LIMIT 1`, sessionID, blobID).Scan(&b.UserID, &b.Data, &b.ObjectKey, &b.MIME, &b.Length, &b.Filename)
	if errors.Is(err, sql.ErrNoRows) {
		return blobRecord{}, ErrBlobNotFound
	}
	return b, err
}

func (r *Repository) DeleteSession(ctx context.Context, id int64) (int64, []pendingBlobDeletion, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var userID int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM user_sessions WHERE id=$1 FOR UPDATE`, id).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
		return 0, nil, ErrSessionNotFound
	} else if err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT b.id,COALESCE(b.object_key,'')
		FROM user_session_blobs b
		JOIN user_session_parts p ON p.blob_id=b.id
		JOIN user_session_turns t ON t.id=p.turn_id
		WHERE t.session_id=$1 AND NOT EXISTS(
			SELECT 1 FROM user_session_parts other_p
			JOIN user_session_turns other_t ON other_t.id=other_p.turn_id
			WHERE other_p.blob_id=b.id AND other_t.session_id<>$1
		)`, id)
	if err != nil {
		return 0, nil, err
	}
	var candidates []pendingBlobDeletion
	for rows.Next() {
		var item pendingBlobDeletion
		if err := rows.Scan(&item.ID, &item.ObjectKey); err != nil {
			rows.Close()
			return 0, nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return 0, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_sessions WHERE id=$1`, id); err != nil {
		return 0, nil, err
	}
	var pending []pendingBlobDeletion
	for _, item := range candidates {
		if strings.TrimSpace(item.ObjectKey) == "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_session_blobs WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM user_session_parts WHERE blob_id=$1)`, item.ID); err != nil {
				return 0, nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user_session_blobs SET storage_state='deletion_pending',deletion_pending_at=NOW() WHERE id=$1 AND NOT EXISTS(SELECT 1 FROM user_session_parts WHERE blob_id=$1)`, item.ID); err != nil {
			return 0, nil, err
		}
		pending = append(pending, item)
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return userID, pending, nil
}

func (r *Repository) FinalizeBlobDeletion(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM user_session_blobs WHERE id=$1 AND storage_state='deletion_pending' AND NOT EXISTS(SELECT 1 FROM user_session_parts WHERE blob_id=$1)`, id)
	return err
}

func (r *Repository) PendingBlobDeletions(ctx context.Context, limit int) ([]pendingBlobDeletion, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id,object_key FROM user_session_blobs WHERE storage_state='deletion_pending' AND object_key IS NOT NULL AND deletion_pending_at < NOW()-INTERVAL '1 minute' ORDER BY deletion_pending_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingBlobDeletion
	for rows.Next() {
		var item pendingBlobDeletion
		if err := rows.Scan(&item.ID, &item.ObjectKey); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) ExpiredSessionIDs(ctx context.Context, before time.Time, limit int) ([]int64, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM user_sessions WHERE updated_at<$1 ORDER BY updated_at,id LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
