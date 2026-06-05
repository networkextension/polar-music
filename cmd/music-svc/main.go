// Command music-svc — the polar-music plugin binary (private music library).
//
// Env vars (mirror the cross-plugin convention used by polar-lawyer /
// polar-expense / polar-buildings / etc.):
//
//	POLAR_MUSIC_DB_DSN          postgres://ideamesh:test123456@127.0.0.1:5432/polar_music?sslmode=disable
//	POLAR_DOCK_BASE             http://127.0.0.1:8080
//	POLAR_PLUGIN_NAME           music
//	POLAR_PLUGIN_TOKEN          polar_plugin_…   (plaintext from /admin-plugins.html)
//	POLAR_MUSIC_LISTEN          127.0.0.1:8104
//	POLAR_MUSIC_VERSION         git-sha or "0.0.1"
//	POLAR_MUSIC_METRICS_TOKEN   bearer for /metrics; unset = 404
//	POLAR_MUSIC_PUBLIC_BASE_URL https://music.4950.store:2443  (for the /api/nav sidebar link)
//
// P0 surface: /healthz + /metrics + /api/* (tracks upload→assets + library
// CRUD). Audio bytes live in polar-assets; this service owns only metadata.

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"

	"github.com/networkextension/polar-music/internal/music"
)

func main() {
	cfg := music.Config{
		DBDSN:         envOrDefault("POLAR_MUSIC_DB_DSN", "postgres://ideamesh:test123456@127.0.0.1:5432/polar_music?sslmode=disable"),
		DockBase:      envOrDefault("POLAR_DOCK_BASE", "http://127.0.0.1:8080"),
		PluginName:    envOrDefault("POLAR_PLUGIN_NAME", "music"),
		PluginToken:   strings.TrimSpace(os.Getenv("POLAR_PLUGIN_TOKEN")),
		Listen:        envOrDefault("POLAR_MUSIC_LISTEN", "127.0.0.1:8104"),
		BuildVersion:  envOrDefault("POLAR_MUSIC_VERSION", "0.0.1"),
		MetricsToken:  strings.TrimSpace(os.Getenv("POLAR_MUSIC_METRICS_TOKEN")),
		PublicBaseURL: strings.TrimSpace(os.Getenv("POLAR_MUSIC_PUBLIC_BASE_URL")),
		// AI 智能歌单 — dock LLM proxy (OpenAI-compatible). Unset = feature 503s.
		LLMProxyURL:   envOrDefault("POLAR_MUSIC_LLM_PROXY_URL", strings.TrimSpace(os.Getenv("POLAR_DOCK_BASE"))+"/api/proxy/v1"),
		LLMProxyToken: strings.TrimSpace(os.Getenv("POLAR_MUSIC_LLM_PROXY_TOKEN")),
		LLMModel:      strings.TrimSpace(os.Getenv("POLAR_MUSIC_LLM_PROXY_MODEL")),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	plugin, err := music.New(ctx, cfg)
	if err != nil {
		log.Fatalf("music: init: %v", err)
	}
	defer plugin.Close()

	r := gin.New()
	r.Use(gin.Recovery())
	plugin.RegisterRoutes(r)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	plugin.Start(ctx)

	go func() {
		log.Printf("music: listening on %s", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("music: serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("music: shutdown")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
