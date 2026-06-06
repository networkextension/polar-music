// Package music is the polar-music plugin — a private, workspace-scoped
// music library. Audio bytes live in polar-assets (Kind=media); this
// service owns only metadata (tracks / albums / artists / playlists).
//
// Init state: DB pool + dock SDK client + heartbeat (nav + OTA) + /healthz
// + /metrics + the /api group. See music-module-design.md for the roadmap.

package music

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	sdk "github.com/networkextension/polar-sdk"
)

type Plugin struct {
	DB         *sql.DB
	Dock       *sdk.Client
	Name       string
	Listen     string
	Ver        string
	MetricsTok string

	publicBaseURL string // POLAR_MUSIC_PUBLIC_BASE_URL — for the /api/nav sidebar link

	// publicWorkspaceID — POLAR_MUSIC_PUBLIC_WORKSPACE_ID. When set, the
	// library READ endpoints (list/detail/stream/cover/albums/artists) are
	// served without login against this workspace, so anyone can browse +
	// play. Empty = library stays fully private (default). Writes (upload,
	// favorites, playlists) always require login regardless.
	publicWorkspaceID string

	llmProxyURL   string // dock LLM proxy (OpenAI-compatible) for AI 智能歌单
	llmProxyToken string
	llmModel      string

	metrics   *musicMetrics
	startedAt time.Time
}

type Config struct {
	DBDSN             string
	DockBase          string
	PluginName        string
	PluginToken       string
	Listen            string
	BuildVersion      string
	MetricsToken      string
	PublicBaseURL     string
	PublicWorkspaceID string
	LLMProxyURL       string
	LLMProxyToken     string
	LLMModel          string
}

func New(ctx context.Context, cfg Config) (*Plugin, error) {
	cfg.PluginName = strings.TrimSpace(cfg.PluginName)
	if cfg.PluginName == "" {
		cfg.PluginName = "music"
	}
	if strings.TrimSpace(cfg.DBDSN) == "" {
		return nil, errors.New("music.New: DBDSN required")
	}
	if strings.TrimSpace(cfg.DockBase) == "" {
		return nil, errors.New("music.New: DockBase required")
	}
	if strings.TrimSpace(cfg.PluginToken) == "" {
		return nil, errors.New("music.New: PluginToken required")
	}

	db, err := sql.Open("postgres", cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open polar_music: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping polar_music: %w", err)
	}

	dock := sdk.NewClient(cfg.DockBase, cfg.PluginName, sdk.DeriveHMACKey(cfg.PluginToken))
	resp, err := dock.Do(http.MethodGet, "/internal/v1/ping", nil)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dock ping: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = db.Close()
		return nil, fmt.Errorf("dock /ping rejected: HTTP %d", resp.StatusCode)
	}

	p := &Plugin{
		DB:                db,
		Dock:              dock,
		Name:              cfg.PluginName,
		Listen:            cfg.Listen,
		Ver:               cfg.BuildVersion,
		MetricsTok:        cfg.MetricsToken,
		publicBaseURL:     strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
		publicWorkspaceID: strings.TrimSpace(cfg.PublicWorkspaceID),
		llmProxyURL:       strings.TrimSpace(cfg.LLMProxyURL),
		llmProxyToken:     strings.TrimSpace(cfg.LLMProxyToken),
		llmModel:          strings.TrimSpace(cfg.LLMModel),
		metrics:           newMusicMetrics(),
		startedAt:         time.Now(),
	}
	if err := p.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

func (p *Plugin) RegisterRoutes(r gin.IRouter) {
	r.GET("/healthz", p.handleHealthz)
	r.GET("/metrics", p.handleMetricsExposition)
	p.registerUIRoutes(r) // self-contained /music.html (no auth; API calls carry the cookie)

	api := r.Group("/api")
	pub := p.optionalWorkspace() // library READ — public when POLAR_MUSIC_PUBLIC_WORKSPACE_ID set, else login
	auth := p.requireWorkspace() // everything that writes / is user-specific — always login
	{
		// Library READ — browseable + playable without login when a public
		// workspace is configured. The 302 stream signs the public workspace's
		// asset, so playback works for anonymous visitors too.
		api.GET("/tracks", pub, p.handleListTracks)
		api.GET("/tracks/:id", pub, p.handleGetTrack)
		api.GET("/tracks/:id/stream", pub, p.handleStreamTrack)
		api.GET("/tracks/:id/cover", pub, p.handleTrackCover)
		api.GET("/albums", pub, p.handleListAlbums)
		api.GET("/artists", pub, p.handleListArtists)

		// Tracks — upload + delete (login required).
		api.POST("/tracks", auth, p.handleUploadTrack)
		api.DELETE("/tracks/:id", auth, p.handleDeleteTrack)

		// Favorites (user-specific → login).
		api.POST("/favorites/:track_id", auth, p.handleAddFavorite)
		api.DELETE("/favorites/:track_id", auth, p.handleRemoveFavorite)

		// Playlists — creating/editing requires login ("创建播放列表需要登陆").
		api.GET("/playlists", auth, p.handleListPlaylists)
		api.POST("/playlists", auth, p.handleCreatePlaylist)
		api.GET("/playlists/:id", auth, p.handleGetPlaylist)
		api.PATCH("/playlists/:id", auth, p.handleUpdatePlaylist)
		api.DELETE("/playlists/:id", auth, p.handleDeletePlaylist)
		api.POST("/playlists/:id/items", auth, p.handleAddPlaylistItem)
		api.DELETE("/playlists/:id/items/:track_id", auth, p.handleRemovePlaylistItem)

		// AI 智能歌单 (P3) — dock LLM proxy; 503 when unconfigured.
		api.POST("/playlists/generate", auth, p.handleGeneratePlaylist)
	}
}

func (p *Plugin) Start(ctx context.Context) {
	go p.heartbeatLoop(ctx)
}

func (p *Plugin) Close() error {
	if p.DB != nil {
		return p.DB.Close()
	}
	return nil
}

func (p *Plugin) handleHealthz(c *gin.Context) {
	dbOK := true
	if err := p.DB.PingContext(c.Request.Context()); err != nil {
		dbOK = false
	}
	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{
		"plugin":         p.Name,
		"version":        p.Ver,
		"uptime_seconds": int64(time.Since(p.startedAt).Seconds()),
		"db_ok":          dbOK,
		"go":             runtime.Version(),
	})
}

func (p *Plugin) handleMetricsExposition(c *gin.Context) {
	if p.MetricsTok == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if c.GetHeader("Authorization") != "Bearer "+p.MetricsTok {
		c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	promhttp.HandlerFor(p.metrics.registry, promhttp.HandlerOpts{}).ServeHTTP(c.Writer, c.Request)
}

func (p *Plugin) heartbeatLoop(ctx context.Context) {
	p.beat(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.beat(ctx)
		}
	}
}

// musicUIRoutes — sidebar entry this plugin contributes to /api/nav.
var musicUIRoutes = []sdk.UIRoute{
	{Path: "/music.html", Label: "乐库", Icon: "music", Order: 75},
}

func (p *Plugin) beat(_ context.Context) {
	err := p.Dock.Heartbeat(sdk.HeartbeatOpts{
		Version:       p.Ver,
		Endpoint:      p.Listen,
		UptimeSeconds: int64(time.Since(p.startedAt).Seconds()),
		PublicBaseURL: p.publicBaseURL,
		UIRoutes:      musicUIRoutes,
	})
	if err != nil {
		log.Printf("music: heartbeat failed: %v", err)
	}
}

type musicMetrics struct {
	registry *prometheus.Registry
	upGauge  prometheus.Gauge
}

func newMusicMetrics() *musicMetrics {
	m := &musicMetrics{registry: prometheus.NewRegistry()}
	m.upGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "polar_music_up",
		Help: "Always 1 while music-svc is serving.",
	})
	m.registry.MustRegister(m.upGauge)
	m.upGauge.Set(1)
	return m
}
