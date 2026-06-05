package music

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf16"
)

// id3Meta is what we lift from an mp3 ID3v2 tag. Non-mp3 files (m4a/flac/…)
// fall back to a "Artist - Title" filename heuristic in the handler.
// Accurate duration/bitrate is deferred to P2 (ffprobe); P0 takes an
// optional client-probed duration via the upload form instead.
type id3Meta struct {
	Title       string
	Artist      string
	AlbumArtist string
	Album       string
	Track       int
	Year        string
	Genre       string
	Cover       []byte
	CoverMime   string
}

// parseID3 reads an ID3v2.2/2.3/2.4 tag from the head of an mp3 file.
// `head` should be the first ~1 MiB (enough to hold the tag + embedded
// cover). Returns ok=false if no "ID3" tag is present. Pure stdlib — no
// external tag library (matches the "pure-Go default" principle and dodges
// the blocked module proxy).
func parseID3(head []byte) (id3Meta, bool) {
	var m id3Meta
	if len(head) < 10 || head[0] != 'I' || head[1] != 'D' || head[2] != '3' {
		return m, false
	}
	ver := head[3]
	tagSize := synchsafe(head[6], head[7], head[8], head[9])
	end := 10 + tagSize
	if end > len(head) {
		end = len(head)
	}
	idLen, szLen := 4, 4
	if ver == 2 { // ID3v2.2: 3-char frame id, 3-byte size
		idLen, szLen = 3, 3
	}
	p := 10
	for p+idLen+szLen <= end {
		id := string(head[p : p+idLen])
		if !isFrameID(id) {
			break
		}
		var size int
		if ver == 4 {
			size = synchsafe(head[p+4], head[p+5], head[p+6], head[p+7])
		} else if ver == 2 {
			size = int(head[p+3])<<16 | int(head[p+4])<<8 | int(head[p+5])
		} else {
			size = int(head[p+4])<<24 | int(head[p+5])<<16 | int(head[p+6])<<8 | int(head[p+7])
		}
		frameHdr := idLen + szLen
		if ver != 2 {
			frameHdr += 2 // v2.3/2.4 have a 2-byte flags field
		}
		fp := p + frameHdr
		if size <= 0 || fp+size > end {
			break
		}
		fr := head[fp : fp+size]
		switch {
		case id == "APIC" || id == "PIC":
			m.Cover, m.CoverMime = readPicture(fr)
		case strings.HasPrefix(id, "T"):
			v := readTextFrame(fr)
			switch id {
			case "TIT2", "TT2":
				m.Title = orStr(v, m.Title)
			case "TPE1", "TP1":
				m.Artist = orStr(v, m.Artist)
			case "TPE2", "TP2":
				m.AlbumArtist = v
			case "TALB", "TAL":
				m.Album = orStr(v, m.Album)
			case "TRCK", "TRK":
				m.Track = parseTrackNo(v)
			case "TYER", "TYE", "TDRC":
				if len(v) >= 4 {
					m.Year = v[:4]
				} else {
					m.Year = v
				}
			case "TCON", "TCO":
				m.Genre = v
			}
		}
		p = fp + size
	}
	return m, true
}

func synchsafe(a, b, c, d byte) int {
	return int(a&0x7f)<<21 | int(b&0x7f)<<14 | int(c&0x7f)<<7 | int(d&0x7f)
}

func isFrameID(s string) bool {
	for _, r := range s {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 0
}

// readTextFrame decodes a text frame: first byte = encoding, rest = text.
func readTextFrame(fr []byte) string {
	if len(fr) < 1 {
		return ""
	}
	enc, data := fr[0], fr[1:]
	return strings.TrimRight(decodeText(enc, data), "\x00")
}

func decodeText(enc byte, data []byte) string {
	switch enc {
	case 1: // UTF-16 with BOM
		return decodeUTF16(data)
	case 2: // UTF-16BE without BOM
		return decodeUTF16BE(data)
	case 3: // UTF-8
		return string(data)
	default: // 0 = ISO-8859-1 (latin1)
		return decodeLatin1(data)
	}
}

func decodeLatin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func decodeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE { // LE BOM
		return decodeUTF16LE(b[2:])
	}
	if len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF { // BE BOM
		return decodeUTF16BE(b[2:])
	}
	return decodeUTF16LE(b)
}

func decodeUTF16LE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

func decodeUTF16BE(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return string(utf16.Decode(u))
}

// readPicture extracts the image bytes + mime from an APIC/PIC frame.
func readPicture(fr []byte) ([]byte, string) {
	if len(fr) < 4 {
		return nil, ""
	}
	enc := fr[0]
	i := 1
	var mime string
	if len(fr) >= 4 && string(fr[1:4]) == "PNG" || (len(fr) >= 4 && string(fr[1:4]) == "JPG") {
		// ID3v2.2 PIC: 3-char image format, not a null-terminated mime
		switch string(fr[1:4]) {
		case "PNG":
			mime = "image/png"
		default:
			mime = "image/jpeg"
		}
		i = 4
	} else {
		// APIC: null-terminated latin1 mime string
		j := i
		for j < len(fr) && fr[j] != 0 {
			j++
		}
		mime = string(fr[i:j])
		if mime == "" {
			mime = "image/jpeg"
		}
		i = j + 1
	}
	if i >= len(fr) {
		return nil, ""
	}
	i++ // picture-type byte
	// description, terminated per encoding
	if enc == 1 || enc == 2 { // UTF-16: double-null terminator
		for i+1 < len(fr) && !(fr[i] == 0 && fr[i+1] == 0) {
			i += 2
		}
		i += 2
	} else { // single-null
		for i < len(fr) && fr[i] != 0 {
			i++
		}
		i++
	}
	if i < 0 || i >= len(fr) {
		return nil, ""
	}
	img := bytes.TrimSpace(fr[i:])
	if len(img) == 0 {
		return nil, ""
	}
	return img, mime
}

func parseTrackNo(s string) int {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '/'); idx >= 0 {
		s = s[:idx]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func orStr(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
