package music

// auth_proxy.go — browser-facing user-session auth, proxied to dock.
//
// The music page is a standalone UI served from its own origin. A fresh
// browser has no dock session cookie and nowhere to log in. These handlers
// let the page log in / inspect the session / log out by forwarding to dock's
// USER-level endpoints (not the plugin HMAC API) and landing the resulting
// access_token cookie on THIS origin — so <audio>/<img> media tags (which
// can't send an Authorization header) keep authenticating via the cookie.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

var authHTTP = &http.Client{Timeout: 15 * time.Second}

// handleLogin proxies POST /api/login to dock; on success it re-issues dock's
// auth cookies on the music origin (Domain stripped → current host).
func (p *Plugin) handleLogin(c *gin.Context) {
	body, _ := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	req, err := http.NewRequest(http.MethodPost, p.dockBase+"/api/login", bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "proxy build failed"})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := authHTTP.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "dock unreachable"})
		return
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		// Re-bind dock's Set-Cookie(s) to the music origin so same-origin
		// fetch + media tags carry them automatically.
		for _, ck := range resp.Cookies() {
			ck.Domain = ""
			ck.Secure = true
			ck.SameSite = http.SameSiteLaxMode
			http.SetCookie(c.Writer, ck)
		}
	}
	c.Data(resp.StatusCode, "application/json; charset=utf-8", rb)
}

// handleMe returns the current user + their workspaces (friendly names), so
// the chip can show "who am I" and a 乐库 switcher. Forwards the inbound
// session (cookie or Bearer) to dock /api/me + /api/teams.
func (p *Plugin) handleMe(c *gin.Context) {
	meBody, code := p.dockGet(c, "/api/me")
	if code/100 != 2 {
		c.Data(code, "application/json; charset=utf-8", meBody)
		return
	}
	var me map[string]any
	_ = json.Unmarshal(meBody, &me)
	out := gin.H{"user": me}
	if teamsBody, tc := p.dockGet(c, "/api/teams"); tc/100 == 2 {
		var t map[string]any
		if json.Unmarshal(teamsBody, &t) == nil {
			out["teams"] = t["teams"]
		}
	}
	c.JSON(http.StatusOK, out)
}

// handleLogout clears the music-origin auth cookies and best-effort revokes
// the dock session.
func (p *Plugin) handleLogout(c *gin.Context) {
	req, _ := http.NewRequest(http.MethodPost, p.dockBase+"/api/logout", nil)
	if ck := c.Request.Header.Get("Cookie"); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if resp, err := authHTTP.Do(req); err == nil {
		resp.Body.Close()
	}
	c.SetCookie("access_token", "", -1, "/", "", true, true)
	c.SetCookie("refresh_token", "", -1, "/api/token", "", true, true)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// dockGet forwards a GET to dock carrying the caller's session (cookie/Bearer).
func (p *Plugin) dockGet(c *gin.Context, path string) ([]byte, int) {
	req, err := http.NewRequest(http.MethodGet, p.dockBase+path, nil)
	if err != nil {
		return []byte(`{"error":"proxy build failed"}`), http.StatusInternalServerError
	}
	if ck := c.Request.Header.Get("Cookie"); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if a := c.GetHeader("Authorization"); a != "" {
		req.Header.Set("Authorization", a)
	}
	resp, err := authHTTP.Do(req)
	if err != nil {
		return []byte(`{"error":"dock unreachable"}`), http.StatusBadGateway
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode
}
