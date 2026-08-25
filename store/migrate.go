package store

import (
	"database/sql"
	"errors"
	"fmt"
)

const (
	createSchemaVersionTableQuery = `
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY
		);
	`
	selectSchemaVersionQuery = `SELECT version FROM schema_version LIMIT 1`
	deleteSchemaVersionQuery = `DELETE FROM schema_version`
	insertSchemaVersionQuery = `INSERT INTO schema_version (version) VALUES (?)`
	appTableExistsQuery      = `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'app'`
)

// migrations are applied in order; ALWAYS append, never insert or edit, since
// deployed databases record how far they got (see STYLEGUIDE.md)
var (
	migrations = []string{
		// 1: the original v0.1 schema
		`
		CREATE TABLE IF NOT EXISTS app (
			name TEXT PRIMARY KEY,
			port INTEGER NOT NULL UNIQUE,
			host TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
	`,
		// 2: users, tokens, keys, settings, app ownership and usage accounting
		`
		CREATE TABLE user (
			id TEXT PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			role TEXT NOT NULL,
			status TEXT NOT NULL,
			app_limit INTEGER,
			memory_mb INTEGER,
			disk_mb INTEGER,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE token (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			prefix TEXT NOT NULL,
			label TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			last_used INTEGER NOT NULL DEFAULT 0
		);
		CREATE INDEX idx_token_user ON token (user_id);
		CREATE TABLE user_key (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			label TEXT NOT NULL,
			key TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE INDEX idx_user_key_user ON user_key (user_id);
		CREATE TABLE app_key (
			app_name TEXT NOT NULL,
			key TEXT NOT NULL
		);
		CREATE INDEX idx_app_key_app ON app_key (app_name);
		CREATE TABLE setting (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		ALTER TABLE app ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE app ADD COLUMN disk_mb INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE app ADD COLUMN over_quota INTEGER NOT NULL DEFAULT 0;
		CREATE INDEX idx_app_owner ON app (owner_id);
	`,
		// 3: app-scoped tokens; an empty app_name keeps the account-wide meaning
		`
		ALTER TABLE token ADD COLUMN app_name TEXT NOT NULL DEFAULT '';
	`,
		// 4: an app's agent token is stored in the clear so its page can always show
		// it (the owner pastes it into a chat, and hiding it after creation only
		// forces regeneration). Account-wide tokens stay hash-only.
		`
		ALTER TABLE token ADD COLUMN secret TEXT NOT NULL DEFAULT '';
	`,
		// 5: email domains whose users are approved on sign-in, no admin in the loop
		`
		CREATE TABLE allowed_domain (
			domain TEXT PRIMARY KEY,
			created_at INTEGER NOT NULL
		);
	`,
		// 6: the built-in assistant's conversation per app, stored as one JSON blob so
		// it survives reloads, restarts and following along from another device
		`
		CREATE TABLE assistant_session (
			app_name TEXT PRIMARY KEY,
			transcript TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		);
	`,
		// Snapshots: one row per btrfs snapshot of an app's home. The subvolume holds
		// the data; this row is the metadata used to list and thin them. auto=1 marks
		// snapshots taken automatically (deploy/turn/hourly), which retention prunes;
		// manual ones (auto=0) are kept.
		`
		CREATE TABLE snapshot (
			id TEXT PRIMARY KEY,
			app_name TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			auto INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX snapshot_app_idx ON snapshot(app_name, created_at);
	`,
		// Custom domains: a hostname that routes to an app on top of its <app>.<base>
		// subdomain. The primary key enforces one app per domain; status drives routing
		// and TLS issuance (pending -> active/error).
		`
		CREATE TABLE app_domain (
			domain TEXT PRIMARY KEY,
			app_name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			last_error TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			active_at INTEGER
		);
		CREATE INDEX app_domain_app_idx ON app_domain(app_name);
	`,
		// The activity log shown in the app's Logs tab: one row per user-initiated
		// action (create, deploy, lifecycle, snapshot, rollback, fork, domain, token).
		// actor is the email that did it (empty for the system/admin token); level is
		// "info" or "error"; detail is a short human sentence.
		`
		CREATE TABLE app_event (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			app_name TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			actor TEXT NOT NULL DEFAULT '',
			level TEXT NOT NULL DEFAULT 'info',
			action TEXT NOT NULL,
			detail TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX app_event_app_idx ON app_event(app_name, created_at);
	`,
		// image_tag pins each app to the workspace image tag it was built with, so
		// changing the base Containerfile only affects new apps and never recreates an
		// existing app's container onto a different image. Empty means "not yet pinned"
		// (an app from before this column); the daemon backfills those on startup.
		`
		ALTER TABLE app ADD COLUMN image_tag TEXT NOT NULL DEFAULT '';
	`,
		// app.id is the app's stable, opaque identity (see the app-id design). name
		// stays the mutable, human-facing label (subdomain, SSH login, display); id is
		// what durable resources key on so a rename is a metadata update, not a move.
		// The index is partial so the existing (empty-id) rows are legal until the
		// daemon backfills them on startup, after which ids are unique.
		`
		ALTER TABLE app ADD COLUMN id TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX app_id_idx ON app(id) WHERE id != '';
	`,
		// Point every per-app table at app.id instead of the app's name, so an app's
		// keys, tokens, assistant transcript, snapshots, domains and activity log stay
		// attached across a rename (the name is looked up from app.id, never stored as
		// the join key). app_name stays as a denormalized mirror for one release as a
		// rollback safety net; the daemon backfills app_id on startup (SQL can't, since
		// the ids themselves are generated in Go). Empty app_id means "not an app row"
		// (account-wide tokens) or "not yet backfilled".
		`
		ALTER TABLE app_key ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE token ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE assistant_session ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE snapshot ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE app_domain ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE app_event ADD COLUMN app_id TEXT NOT NULL DEFAULT '';
		CREATE INDEX idx_app_key_appid ON app_key(app_id);
		CREATE INDEX idx_token_appid ON token(app_id);
		CREATE UNIQUE INDEX assistant_session_appid_idx ON assistant_session(app_id) WHERE app_id != '';
		CREATE INDEX snapshot_appid_idx ON snapshot(app_id);
		CREATE INDEX app_domain_appid_idx ON app_domain(app_id);
		CREATE INDEX app_event_appid_idx ON app_event(app_id);
	`,
		// Built-in assistant token usage, accumulated per app (keyed on app_id, so it
		// survives a rename). Cache tokens are tracked separately because they are
		// priced differently. Summed per owner for the admin view; this is only the
		// built-in assistant, never a tenant's own agent (that bills their account).
		`
		CREATE TABLE app_usage (
			app_id TEXT PRIMARY KEY,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_write_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		);
	`,
		// Per-user assistant permissions: whether the user may pick the External
		// Claude (subscription) mode, and which API models they may use (a
		// comma-separated allowlist; empty means all configured models). A missing row
		// means "the global defaults apply". Kept in its own table so it can be added
		// without touching the user row's column list.
		`
		CREATE TABLE user_assistant (
			user_id TEXT PRIMARY KEY,
			external_allowed INTEGER NOT NULL DEFAULT 0,
			allowed_models TEXT NOT NULL DEFAULT ''
		);
	`,
		// The assistant mode each app last used (External Claude or a model id), keyed
		// on app_id so it survives a rename. Empty/missing means the global default.
		`
		CREATE TABLE app_assistant (
			app_id TEXT PRIMARY KEY,
			mode TEXT NOT NULL DEFAULT ''
		);
	`, // 16: poweroff is explicit, recorded intent. systemd's is-enabled cannot
		// carry it: a template instance that was never enabled reads "disabled" too,
		// so a brand-new app's first seconds looked powered off (and logins/terminals
		// were refused). The daemon backfills existing apps from unit state once.
		`
		ALTER TABLE app ADD COLUMN powered_off INTEGER NOT NULL DEFAULT 0;
	`, // 17: per-app collaborator grants -- users who may work on an app they do
		// not own (everything but delete/rename/collaborator management). Their
		// profile SSH keys join the app's managed authorized_keys while granted.
		`
		CREATE TABLE app_collaborator (
			app_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (app_id, user_id)
		);
		CREATE INDEX idx_app_collaborator_user ON app_collaborator (user_id);
	`, // 18: the base uid of the app's block on its hosting node. Control
		// allocates it (workspace.UIDFor of the app's port) and records it here, so
		// it can reason about an app without asking the node. The daemon backfills
		// existing rows once.
		`
		ALTER TABLE app ADD COLUMN uid INTEGER NOT NULL DEFAULT 0;
	`, // 19: the node registry -- one row per app-running machine. The
		// token_hash/token_expires_at/joined_at columns are from the retired
		// enrollment flow (a node's identity is now a CA-signed certificate named
		// in its config); they stay because migrations are append-only.
		`
		CREATE TABLE node (
			name TEXT PRIMARY KEY,
			address TEXT NOT NULL DEFAULT '',
			token_hash TEXT NOT NULL DEFAULT '',
			token_expires_at INTEGER NOT NULL DEFAULT 0,
			joined_at INTEGER NOT NULL DEFAULT 0,
			last_seen INTEGER NOT NULL DEFAULT 0
		);
	`, // 20: the proxy registry -- one row per data-plane proxy. No address
		// column: a proxy dials control and control never dials back, so the row
		// exists to be a membership switch (and a liveness record), nothing more.
		`
		CREATE TABLE proxy (
			name TEXT PRIMARY KEY,
			registered_at INTEGER NOT NULL DEFAULT 0,
			last_seen INTEGER NOT NULL DEFAULT 0
		);
	`, // 21: what a proxy reports about itself on each heartbeat, so an operator
		// can tell a proxy serving traffic from one that is merely connected.
		`
		ALTER TABLE proxy ADD COLUMN version TEXT NOT NULL DEFAULT '';
		ALTER TABLE proxy ADD COLUMN routes INTEGER NOT NULL DEFAULT 0;
	`, // 22: archiving -- an app shelved rather than deleted. Separate from
		// powered_off, which an owner flips freely: an archived app refuses to power
		// on or deploy at all, and stops taking new snapshots, while its existing
		// ones thin out to monthly rollups.
		`
		ALTER TABLE app ADD COLUMN archived INTEGER NOT NULL DEFAULT 0;
	`,
		// Slot 23 is BURNED: the abandoned connections PoC branch shipped its own
		// entry here (CREATE TABLE connection...) and ran on stage, so databases
		// that ever ran that branch already record version 23. This no-op keeps
		// every history aligned -- a clean database runs nothing real, a
		// PoC-touched one skips exactly the entry it already counted. If
		// connections ever returns, its migration must be re-authored as a NEW
		// entry at the tail (and tolerate its leftover tables).
		`
		SELECT 1;
	`,
		// Per-app resource limit OVERRIDES, admin-set via PATCH .../limits. 0 means
		// no override: memory/disk fall back to the owner's defaults and CPU stays
		// uncapped -- which is also why every existing row starts at 0, keeping
		// upgraded apps on exactly the limits they already had. disk_mb (above)
		// remains USAGE; the limit gets its own column.
		`
		ALTER TABLE app ADD COLUMN memory_limit_mb INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE app ADD COLUMN disk_limit_mb INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE app ADD COLUMN cpu_milli INTEGER NOT NULL DEFAULT 0;
	`,
		// Per-user resource POOLS: the budget an owner's apps draw from (the sum
		// of their apps' effective limits must fit). NULL derives app_limit x the
		// per-app default, which makes an un-pooled user exactly as capable as
		// before pools existed.
		`
		ALTER TABLE user ADD COLUMN memory_pool_mb INTEGER;
		ALTER TABLE user ADD COLUMN disk_pool_mb INTEGER;
	`,
		// The shipped per-app defaults shrank (512/2048 -> 128/256 MB), so every
		// app existing BEFORE the change is pinned at its old effective limit as
		// its own override, and every user who already owns apps gets their old
		// derived budget pinned as an explicit pool -- nothing already deployed
		// changes size, only new apps and new users see the small defaults. The
		// numbers are the OLD built-in defaults by design; a per-user override,
		// where set, wins via the COALESCE.
		`
		UPDATE app SET memory_limit_mb = COALESCE((SELECT u.memory_mb FROM user u WHERE u.id = app.owner_id), 512) WHERE memory_limit_mb = 0;
		UPDATE app SET disk_limit_mb = COALESCE((SELECT u.disk_mb FROM user u WHERE u.id = app.owner_id), 2048) WHERE disk_limit_mb = 0;
		UPDATE user SET memory_pool_mb = COALESCE(memory_pool_mb, COALESCE(app_limit, 3) * COALESCE(memory_mb, 512)) WHERE id IN (SELECT DISTINCT owner_id FROM app WHERE owner_id != '');
		UPDATE user SET disk_pool_mb = COALESCE(disk_pool_mb, COALESCE(app_limit, 3) * COALESCE(disk_mb, 2048)) WHERE id IN (SELECT DISTINCT owner_id FROM app WHERE owner_id != '');
	`,
		// What each member last reported about its own machine (memory, disk,
		// load), as a JSON blob: it is display-only telemetry whose shape may
		// grow, and a blob beats six columns per table plus a migration each
		// time one is added. Empty means "never reported".
		`
		ALTER TABLE node ADD COLUMN stats TEXT NOT NULL DEFAULT '';
		ALTER TABLE proxy ADD COLUMN stats TEXT NOT NULL DEFAULT '';
	`,
		// Whether an app is reachable by anyone with the URL, or only by its
		// owner, its collaborators and admins. Defaulting to 0 is the whole
		// point: every app that predates this column was public, and the
		// migration must not silently change what any of them mean.
		`
		ALTER TABLE app ADD COLUMN private INTEGER NOT NULL DEFAULT 0;
	`,
		// The weaker of the two per-app grants: a viewer may open the app's URL
		// and nothing else. Separate from app_collaborator rather than a role
		// column on it, so neither grant can be read as the other by a query
		// that forgets to filter.
		`
		CREATE TABLE app_viewer (
			app_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (app_id, user_id)
		);
		CREATE INDEX idx_app_viewer_user ON app_viewer (user_id);
	`,
		// Slots 30 and 31 are BURNED, for the same reason slot 23 is: the
		// redirect work (a Domain.RedirectTo column, then an app_redirect table)
		// was built, run on stage, and reverted on 2026-08-22. Stage therefore
		// records version 31 while the code that shipped stops at 29. Two no-ops
		// keep every history aligned -- a clean database runs nothing real here,
		// a stage-touched one skips exactly the entries it already counted. See
		// TODO.md, Deferred.
		`
		SELECT 1;
	`,
		`
		SELECT 1;
	`,
		// Connections and credentials: the accounts and secrets an OWNER attaches
		// once, which their apps can be granted. secret holds ciphertext, never a
		// usable credential; the key lives outside the database.
		//
		// Keyed on an id with a user-chosen slug beside it, so one owner can hold
		// several of the same provider (two Google Calendars) and an app names the
		// one it wants. app_connection therefore points at a connection id, not a
		// provider.
		//
		// The DROPs are deliberate: the abandoned PoC (burned slot 23) left
		// differently-shaped tables of the same name on any database that ran it.
		// They hold nothing anyone can still use -- the key that decrypts them is
		// long gone -- so this reclaims the names rather than migrating them.
		`
		DROP TABLE IF EXISTS app_connection;
		DROP TABLE IF EXISTS connection;
		CREATE TABLE connection (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			slug TEXT NOT NULL,
			provider TEXT NOT NULL,
			kind TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			secret TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '',
			meta TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);
		CREATE UNIQUE INDEX idx_connection_user_slug ON connection (user_id, slug);
		CREATE INDEX idx_connection_user ON connection (user_id);
		CREATE TABLE app_connection (
			app_id TEXT NOT NULL,
			connection_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (app_id, connection_id)
		);
		CREATE INDEX idx_app_connection_conn ON app_connection (connection_id);
	`,

		// Provider definitions: the operator's (owner_id = '') and each user's
		// own. hostit's own catalog stays in Go -- it ships with the binary and
		// has nothing to store.
		//
		// UNIQUE on (owner_id, name) rather than on name alone: two people may
		// each call theirs "acme", because those are two definitions in two
		// namespaces. Refusing that would let one user's choice of name deny it
		// to everybody else on the instance.
		`
		CREATE TABLE provider (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			scopes TEXT NOT NULL DEFAULT '',
			issuer TEXT NOT NULL DEFAULT '',
			auth_url TEXT NOT NULL DEFAULT '',
			token_url TEXT NOT NULL DEFAULT '',
			client_id TEXT NOT NULL DEFAULT '',
			client_secret TEXT NOT NULL DEFAULT '',
			auth_params TEXT NOT NULL DEFAULT '',
			long_lived INTEGER NOT NULL DEFAULT 0,
			help TEXT NOT NULL DEFAULT '',
			name_hint TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		);
		CREATE UNIQUE INDEX idx_provider_owner_name ON provider (owner_id, name);
		CREATE INDEX idx_provider_owner ON provider (owner_id);
	`,
		// Slack's bot provider was renamed from the bare id "slack" to "slack-bot",
		// symmetric with the new "slack-user" personal connection. Rewrite existing
		// bot connections to the new id so they keep resolving to a provider;
		// "slack-user" is an exact non-match and is left alone. Grants live in
		// app_connection keyed by connection id, so they need no change.
		`
		UPDATE connection SET provider = 'slack-bot' WHERE provider = 'slack';
	`,
		// Connection health: 'ok' or 'needs_reconnect'. Control keeps it current
		// by proactively refreshing OAuth tokens; a rejected refresh flips it so
		// the owner sees the connection needs re-authorizing.
		`
		ALTER TABLE connection ADD COLUMN status TEXT NOT NULL DEFAULT 'ok';
	`,
	}
)

// migrate brings the database up to the current schema version, creating it if
// necessary. Databases from before schema_version existed are detected by the
// presence of the app table and treated as version 1.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(createSchemaVersionTableQuery); err != nil {
		return err
	}
	version, err := currentVersion(db)
	if err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		if err := applyMigration(db, i); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and records the new version in the same
// transaction, so a failure leaves the database untouched and a success can
// never be replayed (which would fail with "table already exists")
func applyMigration(db *sql.DB, index int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // No-op once committed
	if _, err := tx.Exec(migrations[index]); err != nil {
		return fmt.Errorf("migration %d failed: %w", index+1, err)
	}
	if _, err := tx.Exec(deleteSchemaVersionQuery); err != nil {
		return err
	}
	if _, err := tx.Exec(insertSchemaVersionQuery, index+1); err != nil {
		return err
	}
	return tx.Commit()
}

// currentVersion returns the schema version, inferring 1 for pre-versioning
// databases that already have an app table, and 0 for empty ones
func currentVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(selectSchemaVersionQuery).Scan(&version)
	if err == nil {
		return version, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	var count int
	if err := db.QueryRow(appTableExistsQuery).Scan(&count); err != nil {
		return 0, err
	}
	if count > 0 {
		return 1, nil
	}
	return 0, nil
}
