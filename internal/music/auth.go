package music

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// requireWorkspace authenticates the browser session against dock and
// resolves the ACTIVE workspace (Bearer access_token OR access_token
// cookie + X-Workspace-Id). Every route is workspace-scoped; the resolved
// workspace is the isolation key the SQL layer enforces.
func (p *Plugin) requireWorkspace() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if tok == "" {
			if ck, err := c.Cookie("access_token"); err == nil {
				tok = strings.TrimSpace(ck)
			}
		}
		if tok == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		res, err := p.Dock.AuthVerifyWS(tok, strings.TrimSpace(c.GetHeader("X-Workspace-Id")))
		if err != nil || res == nil || res.WorkspaceID == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "auth failed"})
			return
		}
		c.Set("workspace_id", res.WorkspaceID)
		c.Set("user_id", res.UserID)
		c.Set("role", res.Role)
		c.Next()
	}
}

// optionalWorkspace resolves the workspace from a session token when one is
// present (so a logged-in user browses THEIR library), but falls back to the
// configured PUBLIC workspace when there's no/invalid token — so the library
// is browseable + playable without login. When no public workspace is
// configured it behaves exactly like requireWorkspace (401 without a valid
// token), i.e. the library stays private by default.
func (p *Plugin) optionalWorkspace() gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if tok == "" {
			if ck, err := c.Cookie("access_token"); err == nil {
				tok = strings.TrimSpace(ck)
			}
		}
		if tok != "" {
			if res, err := p.Dock.AuthVerifyWS(tok, strings.TrimSpace(c.GetHeader("X-Workspace-Id"))); err == nil && res != nil && res.WorkspaceID != "" {
				c.Set("workspace_id", res.WorkspaceID)
				c.Set("user_id", res.UserID)
				c.Set("role", res.Role)
				c.Next()
				return
			}
		}
		if p.publicWorkspaceID != "" {
			c.Set("workspace_id", p.publicWorkspaceID)
			c.Set("user_id", "")
			c.Set("role", "public")
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
	}
}
