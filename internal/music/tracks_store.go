package music

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Track is the metadata row. Audio + cover bytes live in polar-assets;
// AudioAssetID / CoverAssetID reference them.
type Track struct {
	ID           string  `json:"id"`
	WorkspaceID  string  `json:"workspace_id"`
	UploadedBy   string  `json:"uploaded_by"`
	Title        string  `json:"title"`
	Artist       string  `json:"artist"`
	AlbumArtist  string  `json:"album_artist"`
	Album        string  `json:"album"`
	TrackNo      int     `json:"track_no"`
	DiscNo       int     `json:"disc_no"`
	Year         string  `json:"year"`
	Genre        string  `json:"genre"`
	DurationMs   int64   `json:"duration_ms"`
	Codec        string  `json:"codec"`
	Bitrate      int     `json:"bitrate"`
	SizeBytes    int64   `json:"size_bytes"`
	SHA256       string  `json:"sha256"`
	Mime         string  `json:"mime"`
	SourceName   string  `json:"source_filename"`
	AudioAssetID int64   `json:"audio_asset_id"`
	CoverAssetID *int64  `json:"cover_asset_id,omitempty"`
	HasCover     bool    `json:"has_cover"`
	CreatedAt    string  `json:"created_at"`
	Favorited    bool    `json:"favorited,omitempty"`
}

const trackCols = `id, workspace_id, uploaded_by, title, artist, album_artist, album,
	track_no, disc_no, year, genre, duration_ms, codec, bitrate, size_bytes, sha256,
	mime, source_filename, audio_asset_id, cover_asset_id, created_at`

func scanTrack(s interface{ Scan(...any) error }) (*Track, error) {
	var t Track
	var cover sql.NullInt64
	if err := s.Scan(&t.ID, &t.WorkspaceID, &t.UploadedBy, &t.Title, &t.Artist, &t.AlbumArtist,
		&t.Album, &t.TrackNo, &t.DiscNo, &t.Year, &t.Genre, &t.DurationMs, &t.Codec, &t.Bitrate,
		&t.SizeBytes, &t.SHA256, &t.Mime, &t.SourceName, &t.AudioAssetID, &cover, &t.CreatedAt); err != nil {
		return nil, err
	}
	if cover.Valid {
		t.CoverAssetID = &cover.Int64
		t.HasCover = true
	}
	return &t, nil
}

func (p *Plugin) insertTrack(ctx context.Context, t *Track) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO tracks (`+trackCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20, now())`,
		t.ID, t.WorkspaceID, t.UploadedBy, t.Title, t.Artist, t.AlbumArtist, t.Album,
		t.TrackNo, t.DiscNo, t.Year, t.Genre, t.DurationMs, t.Codec, t.Bitrate, t.SizeBytes,
		t.SHA256, t.Mime, t.SourceName, t.AudioAssetID, t.CoverAssetID)
	return err
}

func (p *Plugin) findTrackBySha(ctx context.Context, ws, sha string) (*Track, error) {
	if strings.TrimSpace(sha) == "" {
		return nil, nil
	}
	row := p.DB.QueryRowContext(ctx,
		`SELECT `+trackCols+` FROM tracks WHERE workspace_id=$1 AND sha256=$2`, ws, sha)
	t, err := scanTrack(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (p *Plugin) getTrack(ctx context.Context, ws, id string) (*Track, error) {
	row := p.DB.QueryRowContext(ctx,
		`SELECT `+trackCols+` FROM tracks WHERE workspace_id=$1 AND id=$2`, ws, id)
	t, err := scanTrack(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

// trackFilter drives GET /api/tracks.
type trackFilter struct {
	Q      string
	Album  string
	Artist string
	FavFor string // user_id; non-empty → only favorited tracks
	Limit  int
	Offset int
}

func (p *Plugin) listTracks(ctx context.Context, ws string, f trackFilter) ([]Track, int, error) {
	where := []string{"t.workspace_id = $1"}
	args := []any{ws}
	ph := func(v any) int { args = append(args, v); return len(args) } // bind + return $N

	if s := strings.TrimSpace(f.Q); s != "" {
		i := ph(s)
		where = append(where, fmt.Sprintf(
			"(t.title ILIKE '%%'||$%d||'%%' OR t.artist ILIKE '%%'||$%d||'%%' OR t.album ILIKE '%%'||$%d||'%%')", i, i, i))
	}
	if s := strings.TrimSpace(f.Album); s != "" {
		where = append(where, fmt.Sprintf("t.album = $%d", ph(s)))
	}
	if s := strings.TrimSpace(f.Artist); s != "" {
		i := ph(s)
		where = append(where, fmt.Sprintf("(t.album_artist = $%d OR t.artist = $%d)", i, i))
	}
	favJoin := ""
	if f.FavFor != "" {
		favJoin = fmt.Sprintf("JOIN favorites fv ON fv.track_id=t.id AND fv.workspace_id=t.workspace_id AND fv.user_id=$%d", ph(f.FavFor))
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	if err := p.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM tracks t `+favJoin+` WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	lim, off := 200, f.Offset
	if f.Limit > 0 {
		lim = clamp(f.Limit, 1, 500)
	}
	if off < 0 {
		off = 0
	}
	q := `SELECT ` + qualify(trackCols, "t") + ` FROM tracks t ` + favJoin +
		` WHERE ` + whereSQL + ` ORDER BY t.created_at DESC LIMIT ` + itoa(lim) + ` OFFSET ` + itoa(off)

	rows, err := p.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Track{}
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

func (p *Plugin) deleteTrack(ctx context.Context, ws, id string) (*Track, error) {
	t, err := p.getTrack(ctx, ws, id)
	if err != nil || t == nil {
		return nil, err
	}
	if _, err := p.DB.ExecContext(ctx, `DELETE FROM tracks WHERE workspace_id=$1 AND id=$2`, ws, id); err != nil {
		return nil, err
	}
	_, _ = p.DB.ExecContext(ctx, `DELETE FROM favorites WHERE workspace_id=$1 AND track_id=$2`, ws, id)
	_, _ = p.DB.ExecContext(ctx, `DELETE FROM playlist_items WHERE track_id=$1`, id)
	return t, nil
}

// ── derived album / artist summaries ──────────────────────────────────

type AlbumSummary struct {
	Album       string `json:"album"`
	AlbumArtist string `json:"album_artist"`
	CoverAssetID *int64 `json:"cover_asset_id,omitempty"`
	TrackCount  int    `json:"track_count"`
}

func (p *Plugin) listAlbums(ctx context.Context, ws string) ([]AlbumSummary, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT album,
		       COALESCE(NULLIF(album_artist,''), artist) AS aa,
		       (array_agg(cover_asset_id) FILTER (WHERE cover_asset_id IS NOT NULL))[1] AS cover,
		       count(*)
		  FROM tracks WHERE workspace_id=$1
		 GROUP BY album, aa
		 ORDER BY aa, album`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumSummary{}
	for rows.Next() {
		var a AlbumSummary
		var cover sql.NullInt64
		if err := rows.Scan(&a.Album, &a.AlbumArtist, &cover, &a.TrackCount); err != nil {
			return nil, err
		}
		if cover.Valid {
			a.CoverAssetID = &cover.Int64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type ArtistSummary struct {
	Artist       string `json:"artist"`
	CoverAssetID *int64 `json:"cover_asset_id,omitempty"`
	TrackCount   int    `json:"track_count"`
	AlbumCount   int    `json:"album_count"`
}

func (p *Plugin) listArtists(ctx context.Context, ws string) ([]ArtistSummary, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(album_artist,''), artist) AS aa,
		       (array_agg(cover_asset_id) FILTER (WHERE cover_asset_id IS NOT NULL))[1] AS cover,
		       count(*), count(DISTINCT album)
		  FROM tracks WHERE workspace_id=$1
		 GROUP BY aa ORDER BY aa`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ArtistSummary{}
	for rows.Next() {
		var a ArtistSummary
		var cover sql.NullInt64
		if err := rows.Scan(&a.Artist, &cover, &a.TrackCount, &a.AlbumCount); err != nil {
			return nil, err
		}
		if cover.Valid {
			a.CoverAssetID = &cover.Int64
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// qualify prefixes each comma-separated bare column with "<alias>.".
func qualify(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
