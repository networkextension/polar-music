package music

// Per-workspace 乐库 settings — currently just an "open access" (anonymous
// browse/play) flag. Lets a workspace owner mark THEIR library public so
// visitors can browse + play it without logging in, scoped to that workspace
// (X-Workspace-Id / ?ws=). Complements the single env public workspace.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// isWorkspacePublic reports whether the given workspace's library is marked
// open for anonymous access. Best-effort: any error (no row, DB blip) → false
// (private by default). Called on the public read path, so it must be cheap —
// it's a single PK lookup.
func (p *Plugin) isWorkspacePublic(ws string) bool {
	ws = strings.TrimSpace(ws)
	if ws == "" || p.DB == nil {
		return false
	}
	var b bool
	if err := p.DB.QueryRow(`SELECT is_public FROM music_lib_settings WHERE workspace_id=$1`, ws).Scan(&b); err != nil {
		return false
	}
	return b
}

// getLibPublic returns the stored is_public flag (false when no row yet).
func (p *Plugin) getLibPublic(ws string) (bool, error) {
	var b bool
	err := p.DB.QueryRow(`SELECT is_public FROM music_lib_settings WHERE workspace_id=$1`, strings.TrimSpace(ws)).Scan(&b)
	if err != nil {
		// no row = default private; only a real error propagates
		if strings.Contains(err.Error(), "no rows") {
			return false, nil
		}
		return false, err
	}
	return b, nil
}

// setLibPublic upserts the workspace's is_public flag.
func (p *Plugin) setLibPublic(ws string, pub bool) error {
	_, err := p.DB.Exec(
		`INSERT INTO music_lib_settings (workspace_id, is_public, updated_at)
		 VALUES ($1, $2, now())
		 ON CONFLICT (workspace_id) DO UPDATE SET is_public = EXCLUDED.is_public, updated_at = now()`,
		strings.TrimSpace(ws), pub,
	)
	return err
}

// handleGetLibSettings — GET /api/library/settings (requireWorkspace).
// Returns the active workspace's 乐库 settings so the UI can render the
// "开放访问" toggle in its current state.
func (p *Plugin) handleGetLibSettings(c *gin.Context) {
	ws := c.GetString("workspace_id")
	pub, err := p.getLibPublic(ws)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"workspace_id": ws, "is_public": pub})
}

// handleSetLibSettings — PUT /api/library/settings (requireWorkspace).
// Body: {"is_public": bool}. Scoped to the caller's active workspace (they're
// already a member via requireWorkspace), so a workspace controls its own
// library's openness.
func (p *Plugin) handleSetLibSettings(c *gin.Context) {
	ws := c.GetString("workspace_id")
	if ws == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no workspace"})
		return
	}
	var body struct {
		IsPublic *bool `json:"is_public"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.IsPublic == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "is_public (bool) required"})
		return
	}
	if err := p.setLibPublic(ws, *body.IsPublic); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"workspace_id": ws, "is_public": *body.IsPublic})
}
