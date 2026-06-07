package music

import "context"

// ensureSchema creates the P0 tables. Idempotent (IF NOT EXISTS), runs at
// boot. Audio/cover bytes live in polar-assets; we store only the asset_id
// references. Metadata is denormalised onto `tracks` — albums/artists are
// derived via GROUP BY (P0); dedicated artist/album rows with bio+cover
// editing land in P1 (see music-module-design.md §3).
func (p *Plugin) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tracks (
			id              TEXT PRIMARY KEY,
			workspace_id    TEXT NOT NULL,
			uploaded_by     TEXT NOT NULL DEFAULT '',
			title           TEXT NOT NULL,
			artist          TEXT NOT NULL DEFAULT '未知艺人',
			album_artist    TEXT NOT NULL DEFAULT '',
			album           TEXT NOT NULL DEFAULT '未知专辑',
			track_no        INT  NOT NULL DEFAULT 0,
			disc_no         INT  NOT NULL DEFAULT 0,
			year            TEXT NOT NULL DEFAULT '',
			genre           TEXT NOT NULL DEFAULT '',
			duration_ms     BIGINT NOT NULL DEFAULT 0,
			codec           TEXT NOT NULL DEFAULT '',
			bitrate         INT  NOT NULL DEFAULT 0,
			size_bytes      BIGINT NOT NULL DEFAULT 0,
			sha256          TEXT NOT NULL DEFAULT '',
			mime            TEXT NOT NULL DEFAULT '',
			source_filename TEXT NOT NULL DEFAULT '',
			audio_asset_id  BIGINT NOT NULL,
			cover_asset_id  BIGINT,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		// content-addressed dedup within a workspace
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_ws_sha ON tracks(workspace_id, sha256) WHERE sha256 <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_ws_album  ON tracks(workspace_id, album, album_artist)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_ws_artist ON tracks(workspace_id, album_artist)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_ws_created ON tracks(workspace_id, created_at DESC)`,

		`CREATE TABLE IF NOT EXISTS favorites (
			workspace_id TEXT NOT NULL,
			user_id      TEXT NOT NULL,
			track_id     TEXT NOT NULL,
			created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (workspace_id, user_id, track_id)
		)`,

		`CREATE TABLE IF NOT EXISTS playlists (
			id            TEXT PRIMARY KEY,
			workspace_id  TEXT NOT NULL,
			owner_user_id TEXT NOT NULL,
			name          TEXT NOT NULL,
			description   TEXT NOT NULL DEFAULT '',
			cover_asset_id BIGINT,
			is_public     BOOLEAN NOT NULL DEFAULT false,
			created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_playlists_ws ON playlists(workspace_id, created_at DESC)`,

		`CREATE TABLE IF NOT EXISTS playlist_items (
			playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
			track_id    TEXT NOT NULL,
			ord         INT  NOT NULL DEFAULT 0,
			added_by    TEXT NOT NULL DEFAULT '',
			added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (playlist_id, track_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_playlist_items_pl ON playlist_items(playlist_id, ord)`,

		// Per-workspace 乐库 settings. is_public=true lets anonymous visitors
		// browse + play this workspace's library (scoped by X-Workspace-Id or
		// ?ws=). Replaces relying solely on the single env public workspace.
		`CREATE TABLE IF NOT EXISTS music_lib_settings (
			workspace_id TEXT PRIMARY KEY,
			is_public    BOOLEAN NOT NULL DEFAULT false,
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
	for _, s := range stmts {
		if _, err := p.DB.ExecContext(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
