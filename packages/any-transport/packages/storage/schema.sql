-- YTP Storage Schema — SQLite tables for persistent state.
--
-- All data is local-only. Tokens are NEVER stored here;
-- they go to the OS credential vault.

CREATE TABLE IF NOT EXISTS sessions (
  session_id    TEXT PRIMARY KEY,
  peer_node_id  TEXT NOT NULL,
  my_node_id    TEXT NOT NULL,
  created_at    INTEGER NOT NULL,
  last_active   INTEGER NOT NULL,
  status        TEXT NOT NULL DEFAULT 'active'  -- active, closed, paused
);

CREATE TABLE IF NOT EXISTS epochs (
  session_id    TEXT NOT NULL,
  epoch_id      INTEGER NOT NULL,
  started_at    INTEGER NOT NULL,
  status        TEXT NOT NULL DEFAULT 'active',  -- active, superseded
  PRIMARY KEY (session_id, epoch_id),
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);

CREATE TABLE IF NOT EXISTS streams (
  stream_id     INTEGER PRIMARY KEY,
  session_id    TEXT NOT NULL,
  target        TEXT,           -- e.g. "example.com:443"
  protocol      TEXT DEFAULT 'tcp',
  status        TEXT NOT NULL DEFAULT 'open',  -- open, half-closed, closed
  opened_at     INTEGER NOT NULL,
  closed_at     INTEGER,
  FOREIGN KEY (session_id) REFERENCES sessions(session_id)
);

CREATE TABLE IF NOT EXISTS outbox (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id    TEXT NOT NULL,
  epoch_id      INTEGER NOT NULL,
  direction     TEXT NOT NULL,   -- 'a2b' or 'b2a'
  envelope_seq  INTEGER NOT NULL,  -- seq within epoch/direction
  wire_text     TEXT NOT NULL,     -- full wire-format string
  priority      INTEGER NOT NULL DEFAULT 2,
  sent_at       INTEGER,           -- null if not yet sent
  acked_at      INTEGER,           -- null if not yet acked
  retries       INTEGER NOT NULL DEFAULT 0,
  deadline      INTEGER NOT NULL,
  created_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now') * 1000)
);

CREATE TABLE IF NOT EXISTS inbox (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id    TEXT NOT NULL,
  epoch_id      INTEGER NOT NULL,
  direction     TEXT NOT NULL,
  envelope_seq  INTEGER NOT NULL,
  wire_text     TEXT NOT NULL,
  processed     INTEGER NOT NULL DEFAULT 0,
  received_at   INTEGER NOT NULL DEFAULT (strftime('%s', 'now') * 1000)
);

CREATE TABLE IF NOT EXISTS provider_cursors (
  provider_id   TEXT PRIMARY KEY,
  cursor_value  TEXT,
  updated_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now') * 1000)
);

CREATE TABLE IF NOT EXISTS acks (
  session_id    TEXT NOT NULL,
  direction     TEXT NOT NULL,
  received_up_to INTEGER NOT NULL DEFAULT 0,
  missing       TEXT DEFAULT '[]',  -- JSON array of seq numbers
  updated_at    INTEGER NOT NULL DEFAULT (strftime('%s', 'now') * 1000),
  PRIMARY KEY (session_id, direction)
);

CREATE TABLE IF NOT EXISTS peers (
  node_id       TEXT PRIMARY KEY,
  public_key_ed TEXT NOT NULL,
  public_key_x  TEXT NOT NULL,
  invite_code   TEXT,
  paired_at     INTEGER NOT NULL DEFAULT (strftime('%s', 'now') * 1000),
  last_seen     INTEGER
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_outbox_unsent ON outbox(sent_at) WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_outbox_unacked ON outbox(acked_at) WHERE acked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inbox_unprocessed ON inbox(processed) WHERE processed = 0;
