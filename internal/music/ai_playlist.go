package music

// ai_playlist.go — P3 flagship: AI 智能歌单生成. Feeds the workspace's track
// catalog + a vibe prompt to the dock LLM proxy (OpenAI-compatible), parses
// the model's track selection, and materialises a playlist. Env-gated:
// unconfigured → 503 (core library unaffected). Mirrors polar-buildings'
// dispatch proxy call.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) aiEnabled() bool {
	return p.llmProxyURL != "" && p.llmProxyToken != "" && p.llmModel != ""
}

type generateReq struct {
	Prompt string `json:"prompt"`
}

// POST /api/playlists/generate {prompt}
func (p *Plugin) handleGeneratePlaylist(c *gin.Context) {
	if !p.aiEnabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "AI 未配置（设置 POLAR_MUSIC_LLM_PROXY_URL / _TOKEN / _MODEL）",
		})
		return
	}
	ws, uid := c.GetString("workspace_id"), c.GetString("user_id")
	var req generateReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Prompt) == "" {
		badReq(c, "prompt required")
		return
	}

	tracks, _, err := p.listTracks(c.Request.Context(), ws, trackFilter{Limit: 400})
	if err != nil {
		serverErr(c, err.Error())
		return
	}
	if len(tracks) == 0 {
		badReq(c, "乐库为空，先上传歌曲")
		return
	}

	var catalog strings.Builder
	for i, t := range tracks {
		fmt.Fprintf(&catalog, "%d. %s — %s [%s]%s\n", i+1, t.Title, t.Artist, t.Album, genreSuffix(t.Genre))
		if i >= 300 {
			break
		}
	}
	sys := `你是音乐歌单策划。根据用户描述的情境/心情，从下面这个曲库里挑选并排序合适的曲目，组成一个连贯的歌单。` +
		`只返回 JSON，格式 {"name":"歌单名","indices":[序号,...]}，序号是曲库列表里的数字，挑 8-20 首，按播放顺序排列，不要任何解释或代码块标记。`
	user := "情境：" + req.Prompt + "\n\n曲库：\n" + catalog.String()

	content, err := p.chatComplete(c.Request.Context(), sys, user)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI 调用失败: " + err.Error()})
		return
	}

	var parsed struct {
		Name    string `json:"name"`
		Indices []int  `json:"indices"`
	}
	if err := json.Unmarshal([]byte(extractJSON(content)), &parsed); err != nil || len(parsed.Indices) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI 返回无法解析", "raw": content})
		return
	}

	var ids []string
	seen := map[string]bool{}
	for _, idx := range parsed.Indices {
		if idx >= 1 && idx <= len(tracks) {
			id := tracks[idx-1].ID
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "AI 没选出有效曲目"})
		return
	}

	name := strings.TrimSpace(parsed.Name)
	if name == "" {
		name = "AI · " + req.Prompt
	}
	plID := musicID("pl")
	if _, err := p.DB.ExecContext(c.Request.Context(),
		`INSERT INTO playlists (id, workspace_id, owner_user_id, name, description) VALUES ($1,$2,$3,$4,$5)`,
		plID, ws, uid, name, "AI 生成："+req.Prompt); err != nil {
		serverErr(c, err.Error())
		return
	}
	for i, id := range ids {
		_, _ = p.DB.ExecContext(c.Request.Context(),
			`INSERT INTO playlist_items (playlist_id, track_id, ord, added_by) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			plID, id, i+1, uid)
	}
	c.JSON(http.StatusOK, gin.H{"playlist": gin.H{"id": plID, "name": name, "track_count": len(ids)}})
}

func genreSuffix(g string) string {
	if strings.TrimSpace(g) == "" {
		return ""
	}
	return " 风格:" + g
}

// chatComplete calls the dock LLM proxy (OpenAI-compatible /chat/completions).
func (p *Plugin) chatComplete(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": p.llmModel,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0.7,
	})
	url := strings.TrimRight(p.llmProxyURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.llmProxyToken)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return out.Choices[0].Message.Content, nil
}

// extractJSON pulls a JSON object out of a model reply that may wrap it in
// ```json fences or prose.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	if a := strings.Index(s, "{"); a >= 0 {
		if b := strings.LastIndex(s, "}"); b > a {
			return s[a : b+1]
		}
	}
	return s
}
