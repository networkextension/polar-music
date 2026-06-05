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
