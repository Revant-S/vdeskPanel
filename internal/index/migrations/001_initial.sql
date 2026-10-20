CREATE TABLE objects (
	id               TEXT PRIMARY KEY,
	parent_id        TEXT NOT NULL,
	kind             INTEGER NOT NULL,
	enc_name         TEXT NOT NULL,
	drive_id         TEXT NOT NULL DEFAULT '',
	size             INTEGER NOT NULL DEFAULT 0,
	mtime            INTEGER NOT NULL DEFAULT 0,
	executable       INTEGER NOT NULL DEFAULT 0,
	content_mac      BLOB,
	body_id          BLOB,
	body_md5         TEXT NOT NULL DEFAULT '',
	content_ver      TEXT NOT NULL DEFAULT '',
	placement_ver    TEXT NOT NULL DEFAULT '',
	delete_ver       TEXT NOT NULL DEFAULT '',
	local_state      INTEGER NOT NULL,
	trash_id         TEXT NOT NULL DEFAULT '',
	local_only       INTEGER NOT NULL DEFAULT 0,
	deferred_content BLOB
);
CREATE INDEX objects_parent ON objects(parent_id);
CREATE INDEX objects_content_mac ON objects(content_mac);
CREATE INDEX objects_drive_id ON objects(drive_id);

CREATE TABLE segments (
	object_id TEXT PRIMARY KEY,
	bitmap    BLOB NOT NULL
);

CREATE TABLE pending_ops (
	seq          INTEGER PRIMARY KEY AUTOINCREMENT,
	object_id    TEXT NOT NULL,
	kind         TEXT NOT NULL,
	payload      BLOB,
	stamp        TEXT NOT NULL,
	not_before   INTEGER NOT NULL,
	attempts     INTEGER NOT NULL DEFAULT 0,
	next_attempt INTEGER NOT NULL DEFAULT 0,
	batch_id     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX pending_ops_batch ON pending_ops(batch_id);
CREATE INDEX pending_ops_object ON pending_ops(object_id);

CREATE TABLE outgoing_batches (
	id         TEXT PRIMARY KEY,
	commit_id  TEXT NOT NULL,
	payload    BLOB NOT NULL,
	state      TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE commits (
	id         TEXT PRIMARY KEY,
	device     TEXT NOT NULL,
	clock      TEXT NOT NULL,
	parents    TEXT NOT NULL,
	sealed     BLOB NOT NULL,
	applied    INTEGER NOT NULL DEFAULT 0,
	own        INTEGER NOT NULL DEFAULT 0,
	referenced INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX commits_clock ON commits(clock);
CREATE INDEX commits_device_clock ON commits(device, clock);

CREATE TABLE trash (
	id          TEXT PRIMARY KEY,
	object_id   TEXT NOT NULL,
	enc_path    BLOB NOT NULL,
	deleted_at  INTEGER NOT NULL,
	device      TEXT NOT NULL,
	reason      TEXT NOT NULL,
	size        INTEGER NOT NULL DEFAULT 0,
	file_count  INTEGER NOT NULL DEFAULT 0,
	folder      INTEGER NOT NULL DEFAULT 0,
	content_mac BLOB,
	drive_id    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX trash_content_mac ON trash(content_mac);

CREATE TABLE kv (
	key   TEXT PRIMARY KEY,
	value BLOB NOT NULL
);
