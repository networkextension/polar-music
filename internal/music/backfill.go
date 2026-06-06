package music

// backfill.go — one-shot worker that fills duration (and codec/bitrate) for
// tracks imported before server-side probing existed (e.g. the bulk FLAC
// import, which stored duration_ms=0). It range-fetches each blob's head from
// assets via a dock-signed URL and runs the pure-Go probe (audiometa.go).
//
// Idempotent: it only ever looks at duration_ms=0 rows, so once everything is
// filled it does nothing. Runs in the background from Start(); rate-limited so
// it never floods the assets provider.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const backfillHeadBytes = 1 << 20 // 1 MiB head is enough for FLAC/MP3 headers

func (p *Plugin) backfillDurations(ctx context.Context) {
	// Small initial delay so startup (schema, heartbeat) settles first.
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}

	var fixed, failed int
	for {
		if ctx.Err() != nil {
			return
		}
		tracks, err := p.listZeroDurationTracks(ctx, 50)
		if err != nil {
			log.Printf("music: backfill list failed: %v", err)
			return
		}
		if len(tracks) == 0 {
			if fixed > 0 || failed > 0 {
				log.Printf("music: duration backfill done (fixed=%d failed=%d)", fixed, failed)
			}
			return
		}
		progress := 0
		for i := range tracks {
			if ctx.Err() != nil {
				return
			}
			t := tracks[i]
			pr, err := p.probeTrackFromAssets(ctx, &t)
			if err != nil || pr.DurationMs <= 0 {
				failed++
				if err != nil {
					log.Printf("music: backfill %s probe failed: %v", t.ID, err)
				}
				// Leave the row at 0; the batch-progress guard below stops us
				// from re-selecting an all-unparseable batch forever.
				continue
			}
			if err := p.updateTrackAudioMeta(ctx, t.ID, pr.DurationMs, pr.Codec, pr.Bitrate); err != nil {
				log.Printf("music: backfill %s update failed: %v", t.ID, err)
				failed++
				continue
			}
			fixed++
			progress++
			time.Sleep(150 * time.Millisecond) // be gentle on the provider
		}
		if progress == 0 {
			// Whole batch unparseable — stop rather than spin forever on the
			// same rows (they stay at duration_ms=0; re-runnable after a fix).
			log.Printf("music: duration backfill stalled (fixed=%d failed=%d) — %d rows unparseable", fixed, failed, len(tracks))
			return
		}
	}
}

const backfillFullCap = 64 << 20 // refetch whole file up to this size as a fallback

// probeTrackFromAssets range-fetches the blob head via a dock-signed URL and
// probes it. If the head isn't enough (e.g. an m4a with moov at the tail, or
// a WAV whose data header sits past the window) it refetches the full file
// (capped) and probes again. Uses the track's own workspace for a valid sig.
func (p *Plugin) probeTrackFromAssets(ctx context.Context, t *Track) (audioProbe, error) {
	body, total, err := p.fetchBlob(ctx, t, backfillHeadBytes)
	if err != nil {
		return audioProbe{}, err
	}
	pr := probeAudio(bytes.NewReader(body), total)
	if pr.DurationMs <= 0 && total > int64(len(body)) && total <= backfillFullCap {
		if full, _, ferr := p.fetchBlob(ctx, t, total); ferr == nil {
			pr = probeAudio(bytes.NewReader(full), total)
		}
	}
	return pr, nil
}

// fetchBlob GETs up to n bytes of the track's audio blob via a signed URL,
// returning the body and the blob's total size (from Content-Range or the row).
func (p *Plugin) fetchBlob(ctx context.Context, t *Track, n int64) ([]byte, int64, error) {
	signed, err := p.signedAssetURL(t.AudioAssetID, t.WorkspaceID)
	if err != nil || signed == "" {
		return nil, 0, fmt.Errorf("sign url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, signed, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, 0, fmt.Errorf("blob HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err != nil {
		return nil, 0, err
	}
	total := t.SizeBytes
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		if i := strings.LastIndexByte(cr, '/'); i >= 0 {
			if v, e := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64); e == nil && v > 0 {
				total = v
			}
		}
	}
	if total <= 0 {
		total = int64(len(body))
	}
	return body, total, nil
}
