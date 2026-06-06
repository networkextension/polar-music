# polar-music — API (basic)

REST reference for the core music library: tracks, streaming, albums/artists,
favorites, and playlists. **Out of scope here:** the authentication/login flow
(see the platform auth docs) and the AI smart-playlist endpoint
(`POST /api/playlists/generate`, documented separately).

## Base URL

- Public: `https://music.4950.store:2443`
- Service (local): `http://127.0.0.1:8104`

All endpoints below are under the `/api` prefix.

## Auth & scope (in brief)

Every request is scoped to a **workspace** (`X-Workspace-Id` header; falls back to
the caller's active workspace from the session cookie).

- **Read** endpoints (`GET` library/tracks/albums/artists/stream/cover) are
  **public** when the server has `POLAR_MUSIC_PUBLIC_WORKSPACE_ID` set — they
  serve that workspace's library to anonymous callers. Otherwise they require a
  logged-in session.
- **Write** / user-specific endpoints (upload, delete, favorites, all playlists)
  **always require login**.

## Conventions

- Requests/responses are JSON (`Content-Type: application/json`), except track
  **upload** (multipart) and **stream/cover** (binary via redirect).
- Success: `200 OK` (or `201 Created` on create). Errors:
  `{ "error": "<message>" }` with a 4xx/5xx status.
- Pagination: `limit` + `offset` query params (`limit` omitted/`0` = server default).

## The Track object

```json
{
  "id": "trk_c73dff6b18945c1a",
  "workspace_id": "e6f3b862446e6f211d042c3bd2281ac2",
  "uploaded_by": "vkMPd04LNmGful0D",
  "title": "Bad Habits",
  "artist": "Ed Sheeran",
  "album_artist": "Ed Sheeran",
  "album": "=",
  "track_no": 4,
  "disc_no": 1,
  "year": "2021",
  "genre": "Pop",
  "duration_ms": 231000,
  "codec": "mp3",
  "bitrate": 320,
  "size_bytes": 9241182,
  "sha256": "…",
  "mime": "audio/mpeg",
  "source_filename": "04 Bad Habits.mp3",
  "audio_asset_id": 451,
  "cover_asset_id": 452,
  "has_cover": true,
  "created_at": "2026-06-05T09:50:00Z",
  "favorited": false
}
```

`cover_asset_id` / `favorited` are omitted when absent. Audio/cover bytes are not
inline — fetch them via the stream/cover endpoints.

---

## Library — read

### `GET /api/tracks` — list / search

Query params (all optional): `q` (matches title/artist/album), `album`, `artist`,
`fav=1` (favorites only), `limit`, `offset`.

```json
{ "tracks": [ Track, … ], "total": 618 }
```

### `GET /api/tracks/:id` — one track

```json
{ "track": Track }
```
`404` if not found in the workspace.

### `GET /api/tracks/:id/stream` — audio bytes

`302` redirect to a short-lived signed asset URL for the audio. Supports HTTP
**Range** requests (`206 Partial Content`) so `<audio>` can seek. Use directly as
an `<audio src>`.

### `GET /api/tracks/:id/cover` — cover art

`302` redirect to the signed cover-image URL. `404` if the track has no cover.

### `GET /api/albums` — albums (derived)

```json
{ "albums": [
  { "album": "=", "album_artist": "Ed Sheeran", "cover_asset_id": 452, "track_count": 14 },
  …
] }
```

### `GET /api/artists` — artists (derived)

```json
{ "artists": [
  { "artist": "Ed Sheeran", "cover_asset_id": 452, "track_count": 31, "album_count": 3 },
  …
] }
```

---

## Library — write (login required)

### `POST /api/tracks` — upload

`multipart/form-data`:
- `file` (required): the audio file. Metadata (title/artist/album/year/cover) is
  parsed from its ID3 tags server-side.
- `duration_ms` (optional): client-probed duration, used when tags lack it.

Stored to central assets; **deduplicated by SHA-256** within the workspace.

- New upload → `201 Created` `{ "track": Track }`
- Duplicate (same bytes already present) → `200 OK` `{ "track": Track, "deduped": true }`

### `DELETE /api/tracks/:id`

```json
{ "deleted": "trk_…" }
```
Removes the track row + its audio/cover assets.

---

## Favorites (login required)

### `POST /api/favorites/:track_id`
```json
{ "favorited": true, "track_id": "trk_…" }
```

### `DELETE /api/favorites/:track_id`
```json
{ "favorited": false, "track_id": "trk_…" }
```

(Favorited tracks are also listable via `GET /api/tracks?fav=1`.)

---

## Playlists (login required)

The Playlist object:

```json
{
  "id": "pl_…",
  "workspace_id": "…",
  "owner_user_id": "…",
  "name": "Focus",
  "description": "",
  "cover_asset_id": null,
  "is_public": false,
  "track_count": 12,
  "created_at": "…",
  "updated_at": "…",
  "tracks": [ Track, … ]
}
```
`tracks` is included only on the single-playlist `GET`.

### `GET /api/playlists` — list
```json
{ "playlists": [ Playlist, … ] }
```

### `POST /api/playlists` — create
Body: `{ "name": "Focus", "description": "", "is_public": false }` (`name` required).
→ `201 Created` `{ "playlist": Playlist }`

### `GET /api/playlists/:id` — detail (with tracks)
```json
{ "playlist": Playlist }
```

### `PATCH /api/playlists/:id` — update
Body (all optional): `{ "name": "…", "description": "…", "is_public": true }`.
→ `{ "ok": true }`

### `DELETE /api/playlists/:id`
```json
{ "deleted": "pl_…" }
```

### `POST /api/playlists/:id/items` — add a track
Body: `{ "track_id": "trk_…" }` → `{ "ok": true }`

### `DELETE /api/playlists/:id/items/:track_id` — remove a track
```json
{ "ok": true }
```

---

## Health

- `GET /healthz` → `200` when the service is up.
