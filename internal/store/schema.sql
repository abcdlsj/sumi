-- Sumi v2 SQLite baseline schema (version 4).
-- Any schema change updates this baseline directly and increments schema_meta.version.

CREATE TABLE schema_meta (
    version INTEGER PRIMARY KEY CHECK (version > 0),
    applied_at TEXT NOT NULL
);
INSERT INTO schema_meta (version, applied_at) VALUES (4, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email_normalized TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE browser_sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_browser_sessions_user ON browser_sessions(user_id);

CREATE TABLE spaces (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    accent TEXT NOT NULL DEFAULT 'flame' CHECK (accent IN ('flame', 'gold', 'rose', 'iris', 'sky')),
    owner_member_id TEXT NOT NULL,
    event_seq INTEGER NOT NULL DEFAULT 0 CHECK (event_seq >= 0),
    next_task_number INTEGER NOT NULL DEFAULT 1 CHECK (next_task_number >= 1),
    created_at TEXT NOT NULL
);

CREATE TABLE members (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    user_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('human', 'agent')),
    display_name TEXT NOT NULL,
    access_level TEXT NOT NULL CHECK (access_level IN ('owner', 'admin', 'member')),
    created_at TEXT NOT NULL,
    retired_at TEXT,
    CHECK (lower(display_name) <> 'all'),
    CHECK ((kind = 'human' AND user_id IS NOT NULL) OR (kind = 'agent' AND user_id IS NULL))
);
CREATE UNIQUE INDEX idx_members_space_name ON members(space_id, lower(display_name));
CREATE UNIQUE INDEX idx_members_space_user ON members(space_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX idx_members_one_owner ON members(space_id) WHERE access_level = 'owner';

CREATE TABLE member_permissions (
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    action_code TEXT NOT NULL CHECK (action_code IN ('agent.create', 'channel.create', 'channel.manage_members')),
    granted_by_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (member_id, action_code)
);

CREATE TABLE computers (
    id TEXT PRIMARY KEY,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    hostname TEXT NOT NULL,
    os TEXT NOT NULL CHECK (os IN ('macos', 'linux')),
    token_hash TEXT UNIQUE,
    connection_status TEXT NOT NULL CHECK (connection_status IN ('offline', 'online')),
    daemon_version TEXT NOT NULL DEFAULT '',
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    last_seen_at TEXT,
    created_at TEXT NOT NULL,
    deleted_at TEXT
);
CREATE INDEX idx_computers_owner ON computers(owner_user_id);

CREATE TABLE computer_pairings (
    id TEXT PRIMARY KEY,
    code_hash TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    hostname TEXT NOT NULL,
    os TEXT NOT NULL CHECK (os IN ('macos', 'linux')),
    daemon_version TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'confirmed', 'expired')),
    expires_at TEXT NOT NULL,
    computer_id TEXT UNIQUE REFERENCES computers(id) ON DELETE RESTRICT,
    owner_user_id TEXT REFERENCES users(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    confirmed_at TEXT
);

CREATE TABLE agents (
    member_id TEXT PRIMARY KEY REFERENCES members(id) ON DELETE RESTRICT,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    computer_id TEXT REFERENCES computers(id) ON DELETE RESTRICT,
    role_text TEXT NOT NULL,
    driver_kind TEXT NOT NULL CHECK (driver_kind IN ('acp', 'builtin')),
    driver_config_json TEXT NOT NULL DEFAULT '{}',
    runtime_state TEXT NOT NULL DEFAULT 'offline' CHECK (runtime_state IN ('idle', 'working', 'offline', 'error')),
    focus_conversation_id TEXT,
    last_error_code TEXT NOT NULL DEFAULT '',
    last_error_detail TEXT NOT NULL DEFAULT '' CHECK (length(last_error_detail) <= 240),
    runtime_state_updated_at TEXT,
    created_at TEXT NOT NULL
);
CREATE INDEX idx_agents_computer ON agents(computer_id);

CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('public', 'private', 'dm', 'thread', 'task')),
    slug TEXT,
    topic TEXT NOT NULL DEFAULT '',
    parent_conversation_id TEXT REFERENCES conversations(id) ON DELETE RESTRICT,
    root_message_id TEXT,
    task_id TEXT,
    next_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_seq >= 1),
    created_at TEXT NOT NULL,
    archived_at TEXT,
    CHECK ((kind IN ('public', 'private') AND slug IS NOT NULL) OR (kind NOT IN ('public', 'private') AND slug IS NULL)),
    CHECK ((kind = 'thread' AND parent_conversation_id IS NOT NULL AND root_message_id IS NOT NULL) OR kind <> 'thread'),
    CHECK ((kind = 'task' AND parent_conversation_id IS NOT NULL AND task_id IS NOT NULL) OR kind <> 'task')
);
CREATE UNIQUE INDEX idx_conversations_space_slug ON conversations(space_id, lower(slug)) WHERE slug IS NOT NULL;
CREATE UNIQUE INDEX idx_conversations_thread_root ON conversations(root_message_id) WHERE kind = 'thread';
CREATE UNIQUE INDEX idx_conversations_task ON conversations(task_id) WHERE kind = 'task';

CREATE TABLE conversation_members (
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    joined_at TEXT NOT NULL,
    PRIMARY KEY (conversation_id, member_id)
);
CREATE INDEX idx_conversation_members_member ON conversation_members(member_id);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
    seq INTEGER NOT NULL CHECK (seq > 0),
    author_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK (kind IN ('text', 'action', 'system')),
    body_markdown TEXT NOT NULL DEFAULT '',
    action_kind TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    mention_all INTEGER NOT NULL DEFAULT 0 CHECK (mention_all IN (0, 1)),
    created_at TEXT NOT NULL,
    edited_at TEXT,
    deleted_at TEXT,
    UNIQUE (conversation_id, seq)
);
CREATE INDEX idx_messages_conv_seq ON messages(conversation_id, seq);

CREATE TABLE message_mentions (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    PRIMARY KEY (message_id, member_id)
);
CREATE INDEX idx_message_mentions_member_conv ON message_mentions(member_id, conversation_id, seq);

CREATE TABLE attachments (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    uploader_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    sha256 TEXT NOT NULL,
    object_path TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE message_attachments (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    attachment_id TEXT NOT NULL UNIQUE REFERENCES attachments(id) ON DELETE RESTRICT,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (message_id, attachment_id)
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE RESTRICT,
    number INTEGER NOT NULL CHECK (number > 0),
    channel_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE RESTRICT,
    source_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    conversation_id TEXT UNIQUE REFERENCES conversations(id) ON DELETE SET NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('todo', 'in_progress', 'in_review', 'done', 'closed')),
    creator_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    assignee_member_id TEXT REFERENCES members(id) ON DELETE RESTRICT,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (space_id, number)
);
CREATE INDEX idx_tasks_space_status ON tasks(space_id, status, updated_at DESC);
CREATE INDEX idx_tasks_source_msg ON tasks(source_message_id) WHERE source_message_id IS NOT NULL;
CREATE INDEX idx_tasks_assignee ON tasks(assignee_member_id) WHERE assignee_member_id IS NOT NULL;

CREATE TABLE conversation_states (
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    read_seq INTEGER NOT NULL DEFAULT 0 CHECK (read_seq >= 0),
    notify_level TEXT NOT NULL DEFAULT 'mentions' CHECK (notify_level IN ('all', 'mentions', 'mute')),
    following INTEGER NOT NULL DEFAULT 0 CHECK (following IN (0, 1)),
    done_seq INTEGER NOT NULL DEFAULT 0 CHECK (done_seq >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (conversation_id, member_id)
);
CREATE INDEX idx_conversation_states_member ON conversation_states(member_id);

CREATE TABLE idempotency_records (
    actor_member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    action TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    response_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (actor_member_id, action, idempotency_key)
);

CREATE TABLE events (
    space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence > 0),
    kind TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (space_id, sequence)
);
