CREATE TABLE IF NOT EXISTS user_sessions (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    identity_kind VARCHAR(32) NOT NULL,
    external_session_hash CHAR(64) NOT NULL,
    protocol VARCHAR(64) NOT NULL DEFAULT '',
    model VARCHAR(200) NOT NULL DEFAULT '',
    active_branch_key VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, identity_kind, external_session_hash)
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_updated
    ON user_sessions(user_id, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS user_session_blobs (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind VARCHAR(24) NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    byte_length BIGINT NOT NULL CHECK (byte_length >= 0),
    detected_mime VARCHAR(255) NOT NULL DEFAULT 'application/octet-stream',
    inline_bytes BYTEA,
    object_key TEXT,
    storage_state VARCHAR(24) NOT NULL DEFAULT 'ready',
    deletion_pending_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, kind, content_sha256, byte_length),
    CHECK ((inline_bytes IS NOT NULL) <> (object_key IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_session_blobs_object_key
    ON user_session_blobs(object_key) WHERE object_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_session_turns (
    id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    branch_key VARCHAR(64) NOT NULL,
    parent_turn_id BIGINT REFERENCES user_session_turns(id) ON DELETE SET NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    role VARCHAR(32) NOT NULL,
    content_hash CHAR(64) NOT NULL,
    first_request_id VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (session_id, branch_key, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_user_session_turns_session_branch
    ON user_session_turns(session_id, branch_key, ordinal);

ALTER TABLE user_sessions
    ADD COLUMN IF NOT EXISTS active_head_turn_id BIGINT REFERENCES user_session_turns(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS user_session_parts (
    id BIGSERIAL PRIMARY KEY,
    turn_id BIGINT NOT NULL REFERENCES user_session_turns(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    kind VARCHAR(24) NOT NULL,
    blob_id BIGINT NOT NULL REFERENCES user_session_blobs(id) ON DELETE RESTRICT,
    source_path TEXT NOT NULL DEFAULT '',
    original_filename TEXT NOT NULL DEFAULT '',
    declared_mime VARCHAR(255) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (turn_id, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_user_session_parts_blob ON user_session_parts(blob_id);

CREATE TABLE IF NOT EXISTS user_session_requests (
    id BIGSERIAL PRIMARY KEY,
    session_id BIGINT NOT NULL REFERENCES user_sessions(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id VARCHAR(128) NOT NULL,
    previous_response_id VARCHAR(255) NOT NULL DEFAULT '',
    branch_key VARCHAR(64) NOT NULL,
    head_turn_id BIGINT REFERENCES user_session_turns(id) ON DELETE SET NULL,
    incoming_sequence_hash CHAR(64) NOT NULL,
    reused_turn_count INTEGER NOT NULL DEFAULT 0,
    new_turn_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, request_id)
);

CREATE INDEX IF NOT EXISTS idx_user_session_requests_session_created
    ON user_session_requests(session_id, created_at DESC, id DESC);
