#!/usr/bin/env bash
# upload-music.sh — bulk-upload local audio files to the polar-music 乐库.
#
# Scans a directory for music files, fetches the library's existing tracks,
# and uploads only the ones not already there (deduped by SHA-256 — same
# bytes are skipped even if renamed). Idempotent: re-run anytime.
#
# Usage:
#   ./upload-music.sh [-w|--workspace <id>] <token> [dir]
#     <token>   session access_token (Bearer). Get it from the browser:
#               DevTools → Application → Cookies → access_token, or your CLI login.
#     [dir]     directory to scan (default: current working directory)
#     -w, --workspace <id>   target workspace (sent as X-Workspace-Id) so the
#               import lands in a specific 乐库 instead of the token's active
#               workspace. Flag wins over $POLAR_MUSIC_WORKSPACE.
#
# Env overrides:
#   POLAR_MUSIC_BASE        base URL (default https://music.4950.store:2443)
#   POLAR_MUSIC_WORKSPACE   workspace id (X-Workspace-Id). Fallback when
#                           --workspace is not passed. Omit both to use the
#                           token's active workspace.
#   INSECURE=1              pass curl -k (skip TLS verify; for self-signed dev)
set -euo pipefail

BASE="${POLAR_MUSIC_BASE:-https://music.4950.store:2443}"
WS="${POLAR_MUSIC_WORKSPACE:-}"

# Parse: -w/--workspace <id> flag anywhere; remaining positionals = token, dir.
POS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -w|--workspace)
      [ $# -ge 2 ] || { echo "$1 requires an argument" >&2; exit 2; }
      WS="$2"; shift 2 ;;
    --workspace=*) WS="${1#*=}"; shift ;;
    -h|--help)
      echo "usage: $0 [-w|--workspace <id>] <token> [dir]" >&2; exit 0 ;;
    --) shift; while [ $# -gt 0 ]; do POS+=("$1"); shift; done ;;
    *) POS+=("$1"); shift ;;
  esac
done

TOKEN="${POS[0]:-}"
DIR="${POS[1]:-.}"

if [ -z "$TOKEN" ]; then
  echo "usage: $0 [-w|--workspace <id>] <token> [dir]" >&2
  exit 2
fi
[ -d "$DIR" ] || { echo "not a directory: $DIR" >&2; exit 2; }

CURL=(curl -fsS --connect-timeout 15 -H "Authorization: Bearer $TOKEN")
[ -n "$WS" ] && CURL+=(-H "X-Workspace-Id: $WS")
[ "${INSECURE:-}" = "1" ] && CURL+=(-k)

# sha256 helper (macOS shasum / Linux sha256sum)
if command -v shasum >/dev/null 2>&1; then SHA() { shasum -a 256 "$1" | awk '{print $1}'; }
elif command -v sha256sum >/dev/null 2>&1; then SHA() { sha256sum "$1" | awk '{print $1}'; }
else echo "need shasum or sha256sum" >&2; exit 2; fi

echo "==> library: $BASE   dir: $DIR   workspace: ${WS:-<token active>}"

# 1) pull existing sha256 set (paginate)
echo "==> fetching existing track hashes…"
HAVE="$(mktemp)"; trap 'rm -f "$HAVE"' EXIT
offset=0; limit=500
while :; do
  page="$("${CURL[@]}" "$BASE/api/tracks?limit=$limit&offset=$offset" 2>/dev/null || echo '{}')"
  got="$(printf '%s' "$page" | python3 -c 'import sys,json; print(len((json.load(sys.stdin).get("tracks") or [])))' 2>/dev/null || echo 0)"
  printf '%s' "$page" | python3 -c 'import sys,json
for t in (json.load(sys.stdin).get("tracks") or []):
    if t.get("sha256"): print(t["sha256"])' 2>/dev/null >>"$HAVE" || true
  [ "${got:-0}" -lt "$limit" ] && break
  offset=$((offset+limit))
done
existing=$(sort -u "$HAVE" | wc -l | tr -d ' ')
echo "    library already has $existing track(s)"

# 2) scan + upload
up=0; skip=0; fail=0
while IFS= read -r -d '' f; do
  h="$(SHA "$f")"
  if grep -qxF "$h" "$HAVE" 2>/dev/null; then
    echo "  = skip (exists): $(basename "$f")"; skip=$((skip+1)); continue
  fi
  printf '  + upload: %s … ' "$(basename "$f")"
  if "${CURL[@]}" -X POST "$BASE/api/tracks" -F "file=@$f" >/dev/null 2>&1; then
    echo "ok"; up=$((up+1)); echo "$h" >>"$HAVE"   # avoid re-upload of dup within this run
  else
    echo "FAILED"; fail=$((fail+1))
  fi
done < <(find "$DIR" -type f \( \
  -iname '*.mp3' -o -iname '*.m4a' -o -iname '*.flac' -o -iname '*.wav' \
  -o -iname '*.aac' -o -iname '*.ogg' -o -iname '*.opus' -o -iname '*.aiff' \
  -o -iname '*.aif' -o -iname '*.alac' -o -iname '*.wma' \) -print0)

echo "==> done. uploaded=$up  skipped=$skip  failed=$fail"
