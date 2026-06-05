# polar-music —— 音乐乐库模块设计

> 状态：设计稿（先设计，未动工）
> 日期：2026-06-05
> 对标定位：Polar「面向受监管/敏感行业的私有 AI 数据基础设施」底座上的又一个垂直样板——
> **私有音乐库 / 媒体金库**（自有音乐采集 → 私有多区域存储 → AI 整理/检索/生成歌单 → 协作）。
> 数据不出域，规避公开分发版权问题。

---

## 1. 定位与边界

**polar-music 做什么**：把一个工作区里的音频文件变成一个有结构、可检索、可播放、可被 AI 整理的
私有乐库。典型用法：团队/个人把自有的 mp3/flac/m4a 拖进来，自动解析出
艺人/专辑/曲目/封面/时长，浏览、串流播放、建歌单、收藏，并用 AI 做自动分类、智能歌单、
歌词语义检索、推荐。

**明确不做**（红线，避免变成盗版分发平台）：
- 不做公开内容分发 / 不内置在线曲库爬取。定位是**用户自有内容的私有金库**，与 Polar
  「数据留客户机房、不出域」叙事一致。
- 不做 DRM 破解、不做付费音乐源对接。

**dock 提供、本模块不重造的能力**：身份/工作区/会话鉴权（`AuthVerifyWS`）、
LLM 代理（AI 特性）、平台侧边栏导航（`/api/nav`）、OTA 自更新、RBAC + 资源可见性
（mounts 南北向）。

**本模块自己拥有**：乐库领域数据（artists/albums/tracks/playlists…，自己的 Postgres
`polar_music`）、音频元数据解析、播放/串流编排、AI 乐库特性。**音频字节本身不落本模块磁盘**
——全部走 **polar-assets**（多区域、签名直连、LRU）。

---

## 2. 架构对齐（沿用既有插件范式）

| 维度 | 取值 | 参照 |
|---|---|---|
| 仓库 | `networkextension/polar-music`（独立 repo） | 同 polar-buildings/lawyer |
| 进程 | `cmd/music-svc`，监听 `POLAR_MUSIC_LISTEN`（建议 `127.0.0.1:8104`，部署前确认空闲口） | lawyer=8102 |
| 数据库 | `polar_music`（user `polar`） | 每模块独立库 |
| 子域 | `music.4950.store:2443`（prod）/ `music.dev.4950.store`（dev） | 每模块独立子域 |
| 部署 | 二进制 `/Users/local/.local/bin/music-svc`，launchd `polar.music-svc` | [[feedback-plugin-deploy-paths]] |
| 与 dock 通信 | polar-sdk over HMAC（`/internal/v1/*`） | 同所有模块 |
| 鉴权 | 每请求 `AuthVerifyWS(token, X-Workspace-Id)` → dock `/internal/v1/auth/verify?workspace_id=` | [[project-platform-nav]] active-workspace 修复 |
| 大文件存储 | **polar-assets**（`Kind="media"`，workspace-scoped，private） | sdk `AssetUpload`/`AssetDownloadURL` |
| 前端 | vite + TS 自包含，`mountPlatformNav` 共享侧栏，构建到子域根 | 同 lawyer/expense |
| 导航 | heartbeat 上报 `UIRoutes`（`/music.html`）+ `PublicBaseURL` → `/api/nav` | [[project-platform-nav]] |
| OTA | heartbeat `HeartbeatV2` + `SelfUpdate`（`POLAR_SELF_UPDATE=1` 才武装） | [[project-module-ota]] |
| 权限 | RBAC perm keys + mounts 可见性（见 §10） | [[project-rbac-roadmap]] / [[project-permission-model]] |

插件骨架同 buildings：`New()/Start()/RegisterRoutes()`，后台 `beat()` 心跳。

---

## 3. 数据模型（`polar_music`，全部 workspace 隔离）

所有读写以 `workspace_id` JOIN 隔离（同 buildings/lawyer 的租户隔离）。ID 用 dock 同款短 ID 生成器。

```
artists(
  id, workspace_id, name, sort_name, mbid?,            -- musicbrainz id 可空
  cover_asset_id?, bio?, metadata jsonb, created_at)

albums(
  id, workspace_id, artist_id?, title, album_artist?,
  year?, cover_asset_id?, genre?, metadata jsonb, created_at)

tracks(
  id, workspace_id, album_id?, artist_id?,
  title, track_no?, disc_no?,
  duration_ms?, codec?, bitrate?, sample_rate?, channels?,
  size_bytes, sha256,                                   -- 内容寻址 + 去重
  audio_asset_id,                                       -- → polar-assets（音频字节）
  cover_asset_id?,                                      -- → polar-assets（封面，可继承 album）
  source_filename, lyrics?, lyrics_synced?,             -- 纯文本 / LRC 时间轴
  uploaded_by, metadata jsonb, created_at)

playlists(
  id, workspace_id, owner_user_id, name, description?,
  cover_asset_id?, is_public bool default false, created_at, updated_at)

playlist_items(playlist_id, track_id, ord, added_by, added_at,
  PRIMARY KEY(playlist_id, track_id))

favorites(workspace_id, user_id, track_id, created_at,
  PRIMARY KEY(workspace_id, user_id, track_id))

play_history(                                            -- 推荐/统计的燃料
  id, workspace_id, user_id, track_id, played_at, ms_played, source?)

tags(id, workspace_id, kind, label,                      -- kind: genre|mood|ai
  UNIQUE(workspace_id, kind, label))
track_tags(track_id, tag_id, confidence?, source,        -- source: id3|user|ai
  PRIMARY KEY(track_id, tag_id))

import_jobs(                                              -- 批量导入状态
  id, workspace_id, status, source, total, done, failed,
  error?, created_by, created_at, finished_at)
```

**去重**：`tracks` 同一 workspace 内按 `sha256` 去重（命中则复用同一 audio_asset + track 行），
与 dock attachments 去重一致。封面 asset 也按 sha 去重，专辑级封面共享。

---

## 4. 音频存储与串流（核心，走 polar-assets）

**写**：上传 → 暂存哈希 → `sdk.AssetUpload(AssetUploadInput{WorkspaceID:&ws, Kind:"media",
Name:"music/<sha>", Mime:"audio/…", Metadata:{title,artist,…}}, body io.Reader)` → 拿到
`AssetMeta.ID` 存进 `tracks.audio_asset_id`。**音频字节从不落本模块磁盘**，内存零驻留（流式 body）。

**读/播放**：`GET /api/tracks/:id/stream` → 校验租户 → `sdk.AssetDownloadURL(audio_asset_id)`
拿**短时签名 URL** → `302` 重定向，浏览器 `<audio>`/`<video>` 直连 asset provider 取字节。
dock/本模块**不过音频流量**（多区域 provider 直供，省带宽）。

**HTTP Range / 拖动进度（seek）**：播放器必须支持 Range。两条路径：
- 优选：provider 直连支持 `Range`（206 Partial Content）→ `<audio>` 原生 seek。
  **⚠ 开放项**：需验证 polar-assets provider 的 download 端点是否透传 `Range`；不支持则——
- 兜底：`GET /api/tracks/:id/stream?proxy=1` 由本模块 `AssetDownload` 拉流并自行实现 Range
  （`http.ServeContent` 风格）。代价是流量过模块，仅作 provider 不支持时的退路。

**封面**：同 asset（`Kind="media"`，小图），`GET /api/tracks/:id/cover` → 302 签名 URL，
可设较长签名+前端缓存。

---

## 5. 导入与元数据解析

**单文件 / 拖拽多文件**：`POST /api/tracks`（multipart）或 assets 的 grant+finalize 直传。
入库流水线（事务 + 异步解析，同 lawyer 的 documents 状态机）：

1. 暂存 → 算 sha256 → 去重（命中直接复用，秒回）。
2. **元数据解析**（纯 Go 优先，避免重依赖，同 lawyer「纯 Go 默认、子进程仅在必要时」）：
   - `dhowden/tag`：ID3v2 / MP4(m4a) / FLAC / OGG-Vorbis 标签 + 内嵌封面 → title/artist/
     album/album_artist/track_no/disc_no/year/genre/lyrics/cover。
   - 时长/码率/采样率：`dhowden/tag` 不可靠 → 可选 `ffprobe` 子进程取精确
     `duration_ms/bitrate/sample_rate/codec`（同 expense 的 Vision-OCR、可选外部二进制）。
     无 ffprobe 时降级为「时长未知」，不阻断入库。
3. 内嵌封面 → 单独 `AssetUpload`（去重）→ `cover_asset_id`；无内嵌封面则继承/留空。
4. upsert artist/album（按 name 归一化），插入 track 行，写 `track_tags`(genre, source=id3)。
5. 状态：`import_jobs` 汇总 total/done/failed；单曲 track 行即时可见。

**批量导入**：`POST /api/import`（指向一个已上传的 zip / 一批 asset / 一个目录清单）→ 起
`import_jobs` 后台 goroutine 逐个跑上面的流水线，前端轮询进度。

---

## 6. 检索

- **结构化 + 文本检索**（P0/P2）：`POST /api/search` 按 title/artist/album/genre/tag 做
  Postgres 全文 + ILIKE，租户隔离，分页。
- **语义检索**（P4，可选，复用 [[project-dataflow-rag-platform]]）：把**歌词 + 元数据**经
  polar-dataflow 管线（chunk → bge-m3 embed → pgvector）入库，支持
  「找氛围像深夜 city-pop 的歌」「歌词讲离别的」这种自然语言检索。每工作区独立 pgvector，
  与 lawyer 同套底座。

---

## 7. AI 特性（经 dock LLM 代理，同 buildings 派单 / lawyer）

env 门控 `POLAR_MUSIC_LLM_PROXY_URL/_TOKEN/_MODEL`，未配=AI 特性 503（核心乐库功能不受影响）。

1. **智能歌单生成** `POST /api/playlists/generate`：prompt（「周五下午专注工作，偏氛围/器乐」）
   + 候选曲目元数据（title/artist/genre/tag/时长）喂 LLM → 选曲 + 排序 + 起名 → 落 `playlists`。
   这是最有 demo 张力的特性。
2. **自动打标/分类** `POST /api/tracks/:id/autotag`（或批量）：从元数据（+ 可选音频特征）让 LLM
   给 genre/mood 标签，写 `track_tags`(source=ai, confidence)。补全脏元数据。
3. **歌词** `POST /api/tracks/:id/lyrics:analyze`：分析主题/情绪/翻译/摘要；与 §6 语义检索打通。
4. **推荐** `GET /api/recommendations`：基于 `play_history` + `favorites`，先做简单
   「最近常听的同艺人/同标签」启发式，再可选 LLM 解释「为什么推荐」。
5. **自然语言检索**：把 §6 语义检索包成对话式入口。

所有 AI 调用走 dock 代理 → 自动计费/审计（同 [[project-agent-proxy-and-tasks]]）。

---

## 8. REST 接口（子域 `/api/*`，全部经 `requireAuthViaDock` + workspace）

```
# 曲目 / 导入
POST   /api/tracks                 multipart 上传单曲（或 assets 直传 finalize）
GET    /api/tracks                 列表（filter: album/artist/tag/q, 分页）
GET    /api/tracks/:id             详情
GET    /api/tracks/:id/stream      302 → 签名音频 URL（?proxy=1 走模块 Range 兜底）
GET    /api/tracks/:id/cover       302 → 签名封面 URL
PATCH  /api/tracks/:id             改元数据/歌词
DELETE /api/tracks/:id             删除（解引用 asset；最后引用才真删 blob）
POST   /api/import                 批量导入 → import_jobs
GET    /api/import/:job_id         导入进度

# 专辑 / 艺人
GET    /api/albums                 GET /api/albums/:id
GET    /api/artists                GET /api/artists/:id

# 歌单 / 收藏 / 播放历史
GET/POST/PATCH/DELETE /api/playlists[/:id]
POST/DELETE /api/playlists/:id/items        加/删曲 + 重排序
POST/DELETE /api/favorites/:track_id
POST   /api/scrobble                上报一次播放（写 play_history）

# 检索 / AI
POST   /api/search                  文本/语义检索
POST   /api/playlists/generate      AI 智能歌单
POST   /api/tracks/:id/autotag      AI 打标
POST   /api/tracks/:id/lyrics:analyze
GET    /api/recommendations
```

---

## 9. 前端（vite + TS 自包含，`mountPlatformNav`）

服务于 `music.4950.store`，共享平台侧栏。页面：
- **音乐库**：专辑墙 / 艺人 / 曲目三视图，封面网格，筛选 + 搜索框。
- **常驻播放器**：底部 bar（播放/暂停/进度 seek/音量/队列/随机/循环），用 `<audio>` + 签名 URL；
  曲目结束自动 `scrobble` + 下一曲。
- **歌单**：列表 + 详情 + 拖拽排序；「AI 生成歌单」入口（prompt → 预览 → 保存）。
- **检索**：文本 + 自然语言（语义）两栏。
- **导入**：拖拽多文件 + 进度（轮询 import_jobs）+ 解析结果确认。
- **AI 助手**：智能歌单 / 打标 / 歌词分析的统一入口。

跨子域 cookie 鉴权（dock `POLAR_COOKIE_DOMAIN` 父域）+ `X-Workspace-Id` active-workspace；
工作区切换器同 lawyer。

---

## 10. 权限与可见性

- **RBAC**（[[project-rbac-roadmap]]）：perm keys `music.track.read/write`、`music.playlist.manage`、
  `music.import`、`music.admin`。`gateCan(c, key, enforced)` 门控，flag `POLAR_MUSIC_ENFORCE_RBAC`
  默认关（行为保持）。
- **资源可见性 / mounts（南北向）**（[[project-permission-model]]）：天然契合——一个工作区可把
  另一个工作区的乐库/歌单**只读挂载**进来（「共享曲库」），沿用 workspace_mounts 解析器
  「看得见才挂得上」。
- **公开歌单**：`playlists.is_public` + 复用 dock/assets 的 share-link（`/s/:code`）出只读分享页，
  不暴露原始 asset 永久 URL（仍走短签名）。

---

## 11. 分期计划

| 阶段 | 内容 | 产出 |
|---|---|---|
| **P0** | schema + 上传→assets + 元数据解析(dhowden/tag) + 曲目/专辑/艺人 CRUD + 列表/详情 | 能导入、能看到结构化乐库（还不能播） |
| **P1** | 串流播放（签名 URL + Range，必要时 proxy 兜底）+ Web 播放器 + 歌单 + 收藏 + scrobble | 能听、能建歌单 |
| **P2** | 文本检索 + 批量导入 + import_jobs + ffprobe 精确时长/码率 | 规模化导入 + 搜索 |
| **P3** | AI：智能歌单生成 + 自动打标（dock LLM 代理） | demo 张力特性 |
| **P4** | 歌词语义检索（dataflow/pgvector）+ 推荐（play_history） | 私有 AI 检索/推荐 |
| **P5** | 转码（ffmpeg，FLAC/格式兼容）+ 移动端/Range 加固 | 全格式 + 弱网 |
| **P6** | 部署 zen（music.4950.store）+ nav + RBAC/mounts 接线 | 上线 |

P0–P1 即可做出一个「拖进去就能听、结构清晰」的可演示乐库；P3 起接 AI。

---

## 12. 关键风险 / 开放问题

1. **Range 串流**：必须确认 polar-assets provider download 端点是否透传 HTTP Range（决定能否原生
   seek）。不支持就走 §4 的模块 proxy+ServeContent 兜底（流量过模块）。**P1 前先验证**。
2. **转码成本**：默认存原始字节、按需转码（FLAC→AAC/Opus 给浏览器）。P5 再上 ffmpeg，避免一上来背重依赖。
3. **元数据准确度**：纯 Go `dhowden/tag` 覆盖主流格式；时长/码率交给可选 `ffprobe`，缺失降级不阻断。
4. **版权/合规**：定位私有自有内容金库，不做公开分发；公开歌单仅短签名只读分享。写第一行采集代码前
   这条边界写死（同路线图红线纪律）。
5. **大文件/带宽**：靠 assets 多区域 + 签名直连 provider，dock 不过流量；批量导入用后台 job + 限流。
6. **移动端**：同子域 cookie / Bearer 均支持；未来接 polar-sdk-swift（[[project-mobile-sdk]]）。

---

## 13. 复用关系一览

```
polar-music
 ├─ polar-assets      音频/封面字节（Kind=media，多区域，签名URL，LRU）       ← 核心依赖
 ├─ polar-sdk         AuthVerifyWS / AssetUpload·DownloadURL / Heartbeat·OTA
 ├─ dock LLM 代理      智能歌单 / 打标 / 歌词（计费+审计）
 ├─ polar-dataflow    歌词/元数据 pgvector 语义检索（P4，可选）
 ├─ polar-ui-common   mountPlatformNav 共享侧栏
 ├─ dock RBAC+mounts  权限 + 跨工作区共享曲库
 └─ polar-release/OTA  灰度发布 + 自更新
```

**一句话**：polar-music 是「私有 AI 数据基础设施」底座上的媒体样板——把 assets 的多区域存储、
LLM 代理的 AI、dataflow 的语义检索、RBAC 的协作可见性，组合成一个**拖进去就能听、AI 帮你整理**
的私有乐库。零新基础设施，全是既有能力的垂直拼装。
