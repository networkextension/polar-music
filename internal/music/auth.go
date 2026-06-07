package music

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// cors lets other web apps (e.g. the 灵珠/lzhu music web UI) embed the public
// library from their own origin. It echoes the request Origin (so both
// anonymous and cookie-credentialed reads work — `*` is illegal with
// credentials) and advertises the headers the client needs, incl.
// X-Workspace-Id. Preflight (OPTIONS) is short-circuited 204 by the route.
func (p *Plugin) cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		if origin := strings.TrimSpace(c.GetHeader("Origin")); origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Vary", "Origin")
		} else {
			c.Header("Access-Control-Allow-Origin", "*")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Workspace-Id")
		c.Header("Access-Control-Max-Age", "600")
		c.Next()
	}
}

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
		// Per-workspace open access: if the caller names a workspace (header or
		// ?ws=) whose 乐库 the owner marked public, allow anonymous browse/play
		// scoped to it. <audio>/<img> tags can't send headers, so accept ?ws=.
		reqWS := strings.TrimSpace(c.GetHeader("X-Workspace-Id"))
		if reqWS == "" {
			reqWS = strings.TrimSpace(c.Query("ws"))
		}
		if reqWS != "" && p.isWorkspacePublic(reqWS) {
			c.Set("workspace_id", reqWS)
			c.Set("user_id", "")
			c.Set("role", "public")
			c.Next()
			return
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
