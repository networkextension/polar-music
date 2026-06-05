# polar-music

私有、工作区隔离的**音乐乐库**插件（Polar 平台）。音频字节存中央 **polar-assets**
（`Kind=media`，多区域、签名直连串流）；本服务只拥有元数据
（tracks / albums / artists / playlists）。设计文档见
`doc/music-module-design.md`，本地播放器原型见 `doc/local-yueku-starter.html`。

## P0 已实现

- **上传 → assets**：`POST /api/tracks`（multipart `file` + 可选 `duration_ms`），
  暂存 → sha256 去重 → 解析 ID3v2（标题/艺人/专辑/曲目号/年份/流派 + 内嵌封面）→
  音频与封面分别 `AssetUpload`（tenant/private）→ 落 `tracks` 元数据行。纯 Go ID3
  解析（无外部 tag 库）。
- **乐库读取**：`GET /api/tracks`（q/album/artist/fav 过滤 + 分页）、`/api/tracks/:id`、
  `/api/albums`、`/api/artists`（GROUP BY 派生）。
- **串流**：`GET /api/tracks/:id/stream` → 302 到 `AssetDownloadURL` 短时签名 URL，
  浏览器 `<audio>` 直连 provider（Range 由 provider 支持）。封面同理 `/cover`。
- **收藏 / 歌单**：favorites + playlists/playlist_items CRUD。
- 鉴权：每请求 `AuthVerifyWS(token, X-Workspace-Id)`，workspace 为隔离键。

## 运行

```sh
createdb polar_music   # 或在 dock 后台注册插件拿 token
export POLAR_MUSIC_DB_DSN="postgres://…/polar_music?sslmode=disable"
export POLAR_DOCK_BASE="http://127.0.0.1:8080"
export POLAR_PLUGIN_NAME=music
export POLAR_PLUGIN_TOKEN=polar_plugin_…    # /admin-plugins.html 铸
export POLAR_MUSIC_LISTEN=127.0.0.1:8104
go run ./cmd/music-svc
```

构建：`go build -ldflags="-s -w" -o music-svc ./cmd/music-svc`

## 路线图（doc/music-module-design.md）

P0 元数据+上传+CRUD（本提交）→ P1 Web 播放器+串流加固 → P2 文本检索+批量导入+ffprobe
→ P3 AI 智能歌单+打标 → P4 歌词语义检索+推荐 → P5 转码 → P6 上线 music.4950.store。
