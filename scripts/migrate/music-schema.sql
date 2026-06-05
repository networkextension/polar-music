-- polar-music P0 schema (mirror of internal/music/schema.go ensureSchema).
-- ensureSchema is the source of truth (runs at boot, idempotent); this file
-- is for reference / manual provisioning. DB: polar_music.

CREATE TABLE IF NOT EXISTS tracks (
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
    audio_asset_id  BIGINT NOT NULL,        -- → polar-assets (audio bytes)
    cover_asset_id  BIGINT,                 -- → polar-assets (embedded cover)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tracks_ws_sha ON tracks(workspace_id, sha256) WHERE sha256 <> '';
CREATE INDEX IF NOT EXISTS idx_tracks_ws_album  ON tracks(workspace_id, album, album_artist);
CREATE INDEX IF NOT EXISTS idx_tracks_ws_artist ON tracks(workspace_id, album_artist);
CREATE INDEX IF NOT EXISTS idx_tracks_ws_created ON tracks(workspace_id, created_at DESC);

CREATE TABLE IF NOT EXISTS favorites (
    workspace_id TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    track_id     TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id, track_id)
);

CREATE TABLE IF NOT EXISTS playlists (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT NOT NULL,
    owner_user_id  TEXT NOT NULL,
    name           TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    cover_asset_id BIGINT,
    is_public      BOOLEAN NOT NULL DEFAULT false,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_playlists_ws ON playlists(workspace_id, created_at DESC);

CREATE TABLE IF NOT EXISTS playlist_items (
    playlist_id TEXT NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
    track_id    TEXT NOT NULL,
    ord         INT  NOT NULL DEFAULT 0,
    added_by    TEXT NOT NULL DEFAULT '',
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (playlist_id, track_id)
);
CREATE INDEX IF NOT EXISTS idx_playlist_items_pl ON playlist_items(playlist_id, ord);
