package music

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	sdk "github.com/networkextension/polar-sdk"
)

const maxUploadBytes = 200 << 20 // 200 MiB safety ceiling per track

// POST /api/tracks  (multipart: file=<audio>, duration_ms=<optional client probe>)
//
// Flow: stage → hash → dedup → parse ID3 → upload audio (+cover) to
// polar-assets (Kind=media, private, tenant) → insert metadata row.
func (p *Plugin) handleUploadTrack(c *gin.Context) {
	ws := c.GetString("workspace_id")
	uid := c.GetString("user_id")

	fh, err := c.FormFile("file")
	if err != nil {
		badReq(c, "missing file field")
		return
	}
	if fh.Size <= 0 || fh.Size > maxUploadBytes {
		badReq(c, "file empty or too large")
		return
	}
	ext := strings.ToLower(filepath.Ext(fh.Filename))
	mimeType := fh.Header.Get("Content-Type")
	if mimeType == "" || mimeType == "application/octet-stream" {
		if m := mime.TypeByExtension(ext); m != "" {
			mimeType = m
		} else {
			mimeType = "audio/mpeg"
		}
	}

	// Stage to a temp file so we can hash + parse + stream-upload without
	// holding the whole blob in memory.
	tmp, err := os.CreateTemp("", "music-up-*"+ext)
	if err != nil {
		serverErr(c, "stage failed")
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	src, err := fh.Open()
	if err != nil {
		tmp.Close()
		serverErr(c, "open upload failed")
		return
	}
	h := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(src, h)); err != nil {
		src.Close()
		tmp.Close()
		serverErr(c, "write stage failed")
		return
	}
	src.Close()
	tmp.Close()
	sha := hex.EncodeToString(h.Sum(nil))

	// Dedup: same workspace + same bytes → return the existing track.
	if existing, err := p.findTrackBySha(c.Request.Context(), ws, sha); err == nil && existing != nil {
		c.JSON(http.StatusOK, gin.H{"track": existing, "deduped": true})
		return
	}

	// Parse ID3 from the head (mp3); fall back to "Artist - Title" filename.
	meta := metaFromFilename(fh.Filename)
	if head, err := readHead(tmpPath, 1<<20); err == nil {
		if id3, ok := parseID3(head); ok {
			applyID3(&meta, id3)
		}
	}

	// 1) Upload the audio bytes to central assets (tenant / private).
	audioMeta, err := p.uploadAsset(ws, "music/"+sha+ext, mimeType, tmpPath, map[string]any{
		"title": meta.Title, "artist": meta.Artist, "album": meta.Album,
	})
	if err != nil || audioMeta == nil {
		log.Printf("music: audio asset upload failed ws=%s sha=%s: %v", ws, sha, err)
		serverErr(c, "asset upload failed")
		return
	}

	// 2) Optional embedded cover → its own asset.
	var coverID *int64
	if len(meta.Cover) > 0 {
		if cm, err := p.uploadAssetBytes(ws, "music/"+sha+"/cover", coverMimeOr(meta.CoverMime), meta.Cover); err == nil && cm != nil {
			coverID = &cm.ID
		}
	}

	durMs, _ := strconv.ParseInt(strings.TrimSpace(c.PostForm("duration_ms")), 10, 64)

	t := &Track{
		ID:           musicID("trk"),
		WorkspaceID:  ws,
		UploadedBy:   uid,
		Title:        meta.Title,
		Artist:       meta.Artist,
		AlbumArtist:  firstNonEmpty(meta.AlbumArtist, meta.Artist),
		Album:        meta.Album,
		TrackNo:      meta.Track,
		Year:         meta.Year,
		Genre:        meta.Genre,
		DurationMs:   durMs,
		SizeBytes:    fh.Size,
		SHA256:       sha,
		Mime:         mimeType,
		SourceName:   fh.Filename,
		AudioAssetID: audioMeta.ID,
		CoverAssetID: coverID,
	}
	if err := p.insertTrack(c.Request.Context(), t); err != nil {
		serverErr(c, "db insert failed")
		return
	}
	t.HasCover = coverID != nil
	c.JSON(http.StatusCreated, gin.H{"track": t})
}

// GET /api/tracks
func (p *Plugin) handleListTracks(c *gin.Context) {
	ws := c.GetString("workspace_id")
	f := trackFilter{
		Q:      c.Query("q"),
		Album:  c.Query("album"),
		Artist: c.Query("artist"),
		Limit:  atoiDefault(c.Query("limit"), 0),
		Offset: atoiDefault(c.Query("offset"), 0),
	}
	if c.Query("fav") == "1" {
		f.FavFor = c.GetString("user_id")
	}
	tracks, total, err := p.listTracks(c.Request.Context(), ws, f)
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"tracks": tracks, "total": total})
}

// GET /api/tracks/:id
func (p *Plugin) handleGetTrack(c *gin.Context) {
	t, err := p.getTrack(c.Request.Context(), c.GetString("workspace_id"), c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil {
		notFound(c, "track not found")
		return
	}
	c.JSON(http.StatusOK, gin.H{"track": t})
}

// GET /api/tracks/:id/stream  → 302 to a short-lived signed asset URL.
// The browser <audio> element fetches bytes directly from the provider
// (HTTP Range supported there); dock/this service do not proxy the stream.
func (p *Plugin) handleStreamTrack(c *gin.Context) {
	t, err := p.getTrack(c.Request.Context(), c.GetString("workspace_id"), c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil {
		notFound(c, "track not found")
		return
	}
	signed, err := p.signedAudioURL(t, c.GetString("workspace_id"))
	if err != nil || signed == "" {
		serverErr(c, "could not sign asset url")
		return
	}
	c.Redirect(http.StatusFound, signed)
}

// GET /api/tracks/:id/stream-url → {"url": "<signed asset url>"}. The web
// player points <audio src> straight at this URL instead of the /stream 302:
// Safari won't do HTTP Range streaming THROUGH a redirect — it falls back to
// downloading the whole file (no seek, frozen progress). Same signed + ?ct=
// URL, just returned as JSON so the client sets it directly (Chrome was fine
// either way; this makes Safari stream too).
func (p *Plugin) handleStreamURLTrack(c *gin.Context) {
	t, err := p.getTrack(c.Request.Context(), c.GetString("workspace_id"), c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil {
		notFound(c, "track not found")
		return
	}
	signed, err := p.signedAudioURL(t, c.GetString("workspace_id"))
	if err != nil || signed == "" {
		serverErr(c, "could not sign asset url")
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": signed})
}

// signedAudioURL mints the signed asset URL for a track's audio and appends
// the track's real mime as ?ct= so the blob endpoint serves audio/* instead
// of application/octet-stream (Safari's <audio> refuses octet-stream). The
// blob store is sha256-keyed with no mime of its own.
func (p *Plugin) signedAudioURL(t *Track, ws string) (string, error) {
	signed, err := p.signedAssetURL(t.AudioAssetID, ws)
	if err != nil {
		return "", err
	}
	if signed == "" {
		return "", fmt.Errorf("empty signed url")
	}
	if t.Mime != "" {
		sep := "?"
		if strings.Contains(signed, "?") {
			sep = "&"
		}
		signed += sep + "ct=" + url.QueryEscape(t.Mime)
	}
	return signed, nil
}

// signedAssetURL mints a short-lived signed provider URL for a workspace
// asset. We call dock's /download-url directly (not sdk.AssetDownloadURL)
// because that helper omits workspace_id, so dock's canCallerReadAsset
// rejects private/tenant assets with 404 — it only works for public assets.
func (p *Plugin) signedAssetURL(assetID int64, ws string) (string, error) {
	path := "/internal/v1/assets/" + strconv.FormatInt(assetID, 10) + "/download-url?workspace_id=" + url.QueryEscape(ws)
	resp, err := p.Dock.Do(http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("download-url HTTP %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// GET /api/tracks/:id/cover  → 302 to signed cover URL, or 404.
func (p *Plugin) handleTrackCover(c *gin.Context) {
	t, err := p.getTrack(c.Request.Context(), c.GetString("workspace_id"), c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil || t.CoverAssetID == nil {
		notFound(c, "no cover")
		return
	}
	url, err := p.signedAssetURL(*t.CoverAssetID, c.GetString("workspace_id"))
	if err != nil || url == "" {
		serverErr(c, "could not sign cover url")
		return
	}
	c.Redirect(http.StatusFound, url)
}

// DELETE /api/tracks/:id
func (p *Plugin) handleDeleteTrack(c *gin.Context) {
	ws := c.GetString("workspace_id")
	t, err := p.deleteTrack(c.Request.Context(), ws, c.Param("id"))
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if t == nil {
		notFound(c, "track not found")
		return
	}
	// Best-effort blob cleanup — audio is 1:1 with the track (sha dedup).
	go func(audio int64, cover *int64) {
		if err := p.Dock.AssetDelete(audio); err != nil {
			log.Printf("music: asset delete %d failed: %v", audio, err)
		}
		if cover != nil {
			_ = p.Dock.AssetDelete(*cover)
		}
	}(t.AudioAssetID, t.CoverAssetID)
	c.JSON(http.StatusOK, gin.H{"deleted": t.ID})
}

// ── asset upload helpers ──────────────────────────────────────────────

func (p *Plugin) uploadAsset(ws, name, mimeType, path string, metadata map[string]any) (*sdk.AssetMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return p.Dock.AssetUpload(sdk.AssetUploadInput{
		WorkspaceID: &ws,
		Kind:        "media",
		Visibility:  "private",
		Name:        name,
		Version:     "v1",
		Mime:        mimeType,
		Metadata:    metadata,
	}, f)
}

func (p *Plugin) uploadAssetBytes(ws, name, mimeType string, data []byte) (*sdk.AssetMeta, error) {
	return p.Dock.AssetUpload(sdk.AssetUploadInput{
		WorkspaceID: &ws,
		Kind:        "media",
		Visibility:  "private",
		Name:        name,
		Version:     "v1",
		Mime:        mimeType,
	}, bytes.NewReader(data))
}

// ── small helpers ─────────────────────────────────────────────────────

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	m, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:m], nil
}

func metaFromFilename(name string) id3Meta {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	m := id3Meta{Title: base, Artist: "未知艺人", Album: "未知专辑"}
	if parts := strings.SplitN(base, " - ", 2); len(parts) == 2 {
		m.Artist = strings.TrimSpace(parts[0])
		m.Title = strings.TrimSpace(parts[1])
	}
	return m
}

func applyID3(dst *id3Meta, src id3Meta) {
	dst.Title = firstNonEmpty(src.Title, dst.Title)
	dst.Artist = firstNonEmpty(src.Artist, dst.Artist)
	dst.AlbumArtist = firstNonEmpty(src.AlbumArtist, dst.AlbumArtist)
	dst.Album = firstNonEmpty(src.Album, dst.Album)
	if src.Track > 0 {
		dst.Track = src.Track
	}
	dst.Year = firstNonEmpty(src.Year, dst.Year)
	dst.Genre = firstNonEmpty(src.Genre, dst.Genre)
	if len(src.Cover) > 0 {
		dst.Cover = src.Cover
		dst.CoverMime = src.CoverMime
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func coverMimeOr(m string) string {
	if strings.HasPrefix(m, "image/") {
		return m
	}
	return "image/jpeg"
}
