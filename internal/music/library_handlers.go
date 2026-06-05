package music

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// GET /api/albums  — derived album summaries (cover from first track that has one).
func (p *Plugin) handleListAlbums(c *gin.Context) {
	albums, err := p.listAlbums(c.Request.Context(), c.GetString("workspace_id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"albums": albums})
}

// GET /api/artists — derived artist summaries.
func (p *Plugin) handleListArtists(c *gin.Context) {
	artists, err := p.listArtists(c.Request.Context(), c.GetString("workspace_id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"artists": artists})
}

// POST /api/favorites/:track_id
func (p *Plugin) handleAddFavorite(c *gin.Context) {
	ws, uid := c.GetString("workspace_id"), c.GetString("user_id")
	tid := strings.TrimSpace(c.Param("track_id"))
	t, err := p.getTrack(c.Request.Context(), ws, tid)
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil {
		notFound(c, "track not found")
		return
	}
	if _, err := p.DB.ExecContext(c.Request.Context(),
		`INSERT INTO favorites (workspace_id, user_id, track_id) VALUES ($1,$2,$3)
		 ON CONFLICT DO NOTHING`, ws, uid, tid); err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"favorited": true, "track_id": tid})
}

// DELETE /api/favorites/:track_id
func (p *Plugin) handleRemoveFavorite(c *gin.Context) {
	ws, uid := c.GetString("workspace_id"), c.GetString("user_id")
	tid := strings.TrimSpace(c.Param("track_id"))
	if _, err := p.DB.ExecContext(c.Request.Context(),
		`DELETE FROM favorites WHERE workspace_id=$1 AND user_id=$2 AND track_id=$3`, ws, uid, tid); err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"favorited": false, "track_id": tid})
}
