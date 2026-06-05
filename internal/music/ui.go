package music

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Self-contained P1 UI (no build chain), served at the subdomain root —
// mirrors polar-buildings/lawyer. The page calls the same /api/* this
// service exposes, carrying the dock session cookie (parent-domain) +
// X-Workspace-Id (from ?ws= or localStorage).
//
//go:embed all:assets
var uiFS embed.FS

func (p *Plugin) registerUIRoutes(r gin.IRouter) {
	r.GET("/", func(c *gin.Context) { c.Redirect(http.StatusFound, "/music.html") })
	r.GET("/music.html", func(c *gin.Context) {
		b, err := uiFS.ReadFile("assets/music.html")
		if err != nil {
			c.String(http.StatusNotFound, "ui not embedded")
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	})
}
