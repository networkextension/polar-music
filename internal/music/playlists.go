package music

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type Playlist struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	OwnerUserID string  `json:"owner_user_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	CoverAsset  *int64  `json:"cover_asset_id,omitempty"`
	IsPublic    bool    `json:"is_public"`
	TrackCount  int     `json:"track_count"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	Tracks      []Track `json:"tracks,omitempty"`
}

func scanPlaylist(s interface{ Scan(...any) error }) (*Playlist, error) {
	var pl Playlist
	var cover sql.NullInt64
	if err := s.Scan(&pl.ID, &pl.WorkspaceID, &pl.OwnerUserID, &pl.Name, &pl.Description,
		&cover, &pl.IsPublic, &pl.CreatedAt, &pl.UpdatedAt, &pl.TrackCount); err != nil {
		return nil, err
	}
	if cover.Valid {
		pl.CoverAsset = &cover.Int64
	}
	return &pl, nil
}

const plSelect = `SELECT p.id, p.workspace_id, p.owner_user_id, p.name, p.description,
	p.cover_asset_id, p.is_public, p.created_at, p.updated_at,
	(SELECT count(*) FROM playlist_items i WHERE i.playlist_id = p.id) AS track_count
	FROM playlists p`

// GET /api/playlists
func (p *Plugin) handleListPlaylists(c *gin.Context) {
	rows, err := p.DB.QueryContext(c.Request.Context(),
		plSelect+` WHERE p.workspace_id=$1 ORDER BY p.created_at DESC`, c.GetString("workspace_id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	defer rows.Close()
	out := []Playlist{}
	for rows.Next() {
		pl, err := scanPlaylist(rows)
		if err != nil {
			serverErr(c, err.Error())
			return
		}
		out = append(out, *pl)
	}
	c.JSON(http.StatusOK, gin.H{"playlists": out})
}

// POST /api/playlists  {name, description?}
func (p *Plugin) handleCreatePlaylist(c *gin.Context) {
	ws, uid := c.GetString("workspace_id"), c.GetString("user_id")
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IsPublic    bool   `json:"is_public"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		badReq(c, "name required")
		return
	}
	id := musicID("pl")
	if _, err := p.DB.ExecContext(c.Request.Context(),
		`INSERT INTO playlists (id, workspace_id, owner_user_id, name, description, is_public)
		 VALUES ($1,$2,$3,$4,$5,$6)`, id, ws, uid, strings.TrimSpace(in.Name), in.Description, in.IsPublic); err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusCreated, gin.H{"playlist": Playlist{
		ID: id, WorkspaceID: ws, OwnerUserID: uid, Name: in.Name, Description: in.Description, IsPublic: in.IsPublic,
	}})
}

// GET /api/playlists/:id  (with ordered tracks)
func (p *Plugin) handleGetPlaylist(c *gin.Context) {
	ws := c.GetString("workspace_id")
	row := p.DB.QueryRowContext(c.Request.Context(), plSelect+` WHERE p.workspace_id=$1 AND p.id=$2`, ws, c.Param("id"))
	pl, err := scanPlaylist(row)
	if err == sql.ErrNoRows {
		notFound(c, "playlist not found")
		return
	}
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	rows, err := p.DB.QueryContext(c.Request.Context(),
		`SELECT `+qualify(trackCols, "t")+` FROM playlist_items i
		   JOIN tracks t ON t.id = i.track_id
		  WHERE i.playlist_id=$1 AND t.workspace_id=$2
		  ORDER BY i.ord, i.added_at`, pl.ID, ws)
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	defer rows.Close()
	pl.Tracks = []Track{}
	for rows.Next() {
		t, err := scanTrack(rows)
		if err != nil {
			serverErr(c, err.Error())
			return
		}
		pl.Tracks = append(pl.Tracks, *t)
	}
	c.JSON(http.StatusOK, gin.H{"playlist": pl})
}

// PATCH /api/playlists/:id  {name?, description?, is_public?}
func (p *Plugin) handleUpdatePlaylist(c *gin.Context) {
	ws := c.GetString("workspace_id")
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		IsPublic    *bool   `json:"is_public"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badReq(c, "bad json")
		return
	}
	res, err := p.DB.ExecContext(c.Request.Context(), `
		UPDATE playlists SET
			name = COALESCE($3, name),
			description = COALESCE($4, description),
			is_public = COALESCE($5, is_public),
			updated_at = now()
		WHERE workspace_id=$1 AND id=$2`, ws, c.Param("id"), in.Name, in.Description, in.IsPublic)
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(c, "playlist not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /api/playlists/:id
func (p *Plugin) handleDeletePlaylist(c *gin.Context) {
	res, err := p.DB.ExecContext(c.Request.Context(),
		`DELETE FROM playlists WHERE workspace_id=$1 AND id=$2`, c.GetString("workspace_id"), c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		notFound(c, "playlist not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": c.Param("id")})
}

// POST /api/playlists/:id/items  {track_id}
func (p *Plugin) handleAddPlaylistItem(c *gin.Context) {
	ws, uid := c.GetString("workspace_id"), c.GetString("user_id")
	plID := c.Param("id")
	var in struct {
		TrackID string `json:"track_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.TrackID) == "" {
		badReq(c, "track_id required")
		return
	}
	// Both playlist and track must belong to this workspace.
	if !p.ownsPlaylist(c, ws, plID) {
		notFound(c, "playlist not found")
		return
	}
	t, err := p.getTrack(c.Request.Context(), ws, in.TrackID)
	if err != nil || t == nil {
		notFound(c, "track not found")
		return
	}
	if _, err := p.DB.ExecContext(c.Request.Context(), `
		INSERT INTO playlist_items (playlist_id, track_id, ord, added_by)
		VALUES ($1,$2,(SELECT COALESCE(max(ord),0)+1 FROM playlist_items WHERE playlist_id=$1),$3)
		ON CONFLICT DO NOTHING`, plID, in.TrackID, uid); err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// DELETE /api/playlists/:id/items/:track_id
func (p *Plugin) handleRemovePlaylistItem(c *gin.Context) {
	ws := c.GetString("workspace_id")
	plID := c.Param("id")
	if !p.ownsPlaylist(c, ws, plID) {
		notFound(c, "playlist not found")
		return
	}
	if _, err := p.DB.ExecContext(c.Request.Context(),
		`DELETE FROM playlist_items WHERE playlist_id=$1 AND track_id=$2`, plID, c.Param("track_id")); err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (p *Plugin) ownsPlaylist(c *gin.Context, ws, plID string) bool {
	var x string
	err := p.DB.QueryRowContext(c.Request.Context(),
		`SELECT id FROM playlists WHERE workspace_id=$1 AND id=$2`, ws, plID).Scan(&x)
	return err == nil
}
