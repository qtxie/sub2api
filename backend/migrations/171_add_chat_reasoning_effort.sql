ALTER TABLE chat_conversations
    ADD COLUMN IF NOT EXISTS reasoning_effort VARCHAR(32) NOT NULL DEFAULT '';
