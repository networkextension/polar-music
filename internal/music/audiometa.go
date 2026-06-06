package music

// audiometa.go — pure-Go audio duration/codec/bitrate probing. No external
// deps (ffprobe etc.) so it works behind the blocked module proxy and needs
// nothing installed on the host. FLAC is exact (STREAMINFO); MP3 uses the
// Xing/Info VBR header or a CBR estimate; MP4/M4A reads mvhd. Anything we
// can't parse returns a zero probe and the caller leaves the field at 0.

import (
	"encoding/binary"
	"io"
)

type audioProbe struct {
	DurationMs int64
	SampleRate int
	Bitrate    int // kbps
	Codec      string
}

// probeAudio reads the header region of r (size = total bytes) and returns
// what it can decode. It only reads the head for FLAC/MP3; MP4 may walk
// top-level boxes (which can sit at the tail) via the ReaderAt.
func probeAudio(r io.ReaderAt, size int64) audioProbe {
	head := make([]byte, min64(size, 64<<10))
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	if len(head) < 12 {
		return audioProbe{}
	}
	switch {
	case head[0] == 'f' && head[1] == 'L' && head[2] == 'a' && head[3] == 'C':
		return probeFLAC(head)
	case string(head[4:8]) == "ftyp":
		return probeMP4(r, size)
	case head[0] == 'I' && head[1] == 'D' && head[2] == '3':
		return probeMP3(r, head, size)
	case head[0] == 0xFF && head[1]&0xE0 == 0xE0:
		return probeMP3(r, head, size)
	case string(head[0:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return probeWAV(r, size)
	default:
		return audioProbe{}
	}
}

// ── WAV / RIFF ────────────────────────────────────────────────────────
// duration = data-chunk size / byte-rate (both from the fmt + data chunk
// headers near the front). Exact for PCM.
func probeWAV(r io.ReaderAt, size int64) audioProbe {
	p := audioProbe{Codec: "wav"}
	var byteRate int64
	pos := int64(12) // skip "RIFF"<size>"WAVE"
	hdr := make([]byte, 8)
	for pos+8 <= size {
		if _, err := r.ReadAt(hdr, pos); err != nil {
			break
		}
		id := string(hdr[0:4])
		clen := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		if id == "fmt " {
			fmtb := make([]byte, 16)
			if _, err := r.ReadAt(fmtb, pos+8); err == nil {
				p.SampleRate = int(binary.LittleEndian.Uint32(fmtb[4:8]))
				byteRate = int64(binary.LittleEndian.Uint32(fmtb[8:12]))
			}
		} else if id == "data" {
			if byteRate > 0 {
				p.DurationMs = clen * 1000 / byteRate
			}
			return p
		}
		pos += 8 + clen + (clen & 1) // chunks are word-aligned
	}
	return p
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// ── FLAC ──────────────────────────────────────────────────────────────
// "fLaC" + metadata blocks; STREAMINFO (type 0) is always first, 34 bytes.
// The packed field gives sample_rate (20b), channels (3b), bps (5b),
// total_samples (36b). duration = total_samples / sample_rate. Exact.
func probeFLAC(head []byte) audioProbe {
	// STREAMINFO data begins after "fLaC"(4) + block header(4) = offset 8.
	// The sampleRate/totalSamples field begins 10 bytes into that data.
	const off = 4 + 4 + 10
	if len(head) < off+8 {
		return audioProbe{Codec: "flac"}
	}
	b := head[off : off+8]
	sampleRate := int(b[0])<<12 | int(b[1])<<4 | int(b[2])>>4
	totalSamples := uint64(b[3]&0x0F)<<32 | uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	p := audioProbe{Codec: "flac", SampleRate: sampleRate}
	if sampleRate > 0 && totalSamples > 0 {
		p.DurationMs = int64(totalSamples) * 1000 / int64(sampleRate)
	}
	return p
}

// ── MP3 ───────────────────────────────────────────────────────────────
var mp3BitrateV1L3 = []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
var mp3BitrateV2L3 = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
var mp3SampleRates = map[int][]int{
	3: {44100, 48000, 32000}, // MPEG1
	2: {22050, 24000, 16000}, // MPEG2
	0: {11025, 12000, 8000},  // MPEG2.5
}

func probeMP3(r io.ReaderAt, head []byte, size int64) audioProbe {
	// Skip an ID3v2 tag if present.
	start := 0
	if len(head) >= 10 && head[0] == 'I' && head[1] == 'D' && head[2] == '3' {
		start = 10 + synchsafe(head[6], head[7], head[8], head[9])
	}
	// Re-read from the first frame if it's past our head buffer.
	frame := head
	base := 0
	if start+4 > len(head) {
		buf := make([]byte, 4096)
		n, _ := r.ReadAt(buf, int64(start))
		frame = buf[:n]
		base = start
		start = 0
	}
	// Scan a little for the frame sync (some files pad after the tag).
	fh := -1
	for i := start; i+4 <= len(frame); i++ {
		if frame[i] == 0xFF && frame[i+1]&0xE0 == 0xE0 {
			fh = i
			break
		}
	}
	if fh < 0 {
		return audioProbe{Codec: "mp3"}
	}
	h := frame[fh : fh+4]
	verBits := (h[1] >> 3) & 0x03 // 3=MPEG1, 2=MPEG2, 0=MPEG2.5
	brIdx := int(h[2]>>4) & 0x0F
	srIdx := int(h[2]>>2) & 0x03
	srs, ok := mp3SampleRates[int(verBits)]
	if !ok || srIdx >= len(srs) {
		return audioProbe{Codec: "mp3"}
	}
	sampleRate := srs[srIdx]
	var bitrate int
	if verBits == 3 {
		bitrate = mp3BitrateV1L3[brIdx]
	} else {
		bitrate = mp3BitrateV2L3[brIdx]
	}
	samplesPerFrame := 1152
	if verBits != 3 {
		samplesPerFrame = 576
	}
	p := audioProbe{Codec: "mp3", SampleRate: sampleRate, Bitrate: bitrate}

	// Xing/Info VBR header → exact frame count.
	channels := (h[3] >> 6) & 0x03
	xingOff := fh + 4
	if verBits == 3 {
		xingOff += map[bool]int{true: 17, false: 32}[channels == 3]
	} else {
		xingOff += map[bool]int{true: 9, false: 17}[channels == 3]
	}
	if xingOff+12 <= len(frame) {
		tag := string(frame[xingOff : xingOff+4])
		if tag == "Xing" || tag == "Info" {
			flags := binary.BigEndian.Uint32(frame[xingOff+4 : xingOff+8])
			if flags&0x1 != 0 { // frames field present
				frames := binary.BigEndian.Uint32(frame[xingOff+8 : xingOff+12])
				if sampleRate > 0 {
					p.DurationMs = int64(frames) * int64(samplesPerFrame) * 1000 / int64(sampleRate)
					return p
				}
			}
		}
	}
	// CBR estimate from file size and frame bitrate.
	if bitrate > 0 {
		audioBytes := size - int64(base) - int64(start)
		if audioBytes > 0 {
			p.DurationMs = audioBytes * 8 / int64(bitrate) // ms = bytes*8 / kbps
		}
	}
	return p
}

// ── MP4 / M4A ─────────────────────────────────────────────────────────
// Walk top-level boxes to find moov→mvhd. moov may be at the tail (non
// fast-start files), so we use the ReaderAt to seek rather than the head.
func probeMP4(r io.ReaderAt, size int64) audioProbe {
	moovOff, moovSize, ok := findBox(r, 0, size, "moov")
	if !ok {
		return audioProbe{Codec: "m4a"}
	}
	mvhdOff, _, ok := findBox(r, moovOff+8, moovOff+moovSize, "mvhd")
	if !ok {
		return audioProbe{Codec: "m4a"}
	}
	b := make([]byte, 32)
	n, _ := r.ReadAt(b, mvhdOff+8) // skip box header (size+type)
	if n < 20 {
		return audioProbe{Codec: "m4a"}
	}
	version := b[0]
	var timescale, durationUnits uint64
	if version == 1 {
		if n < 28 {
			return audioProbe{Codec: "m4a"}
		}
		timescale = uint64(binary.BigEndian.Uint32(b[20:24]))
		durationUnits = binary.BigEndian.Uint64(b[24:32])
	} else {
		timescale = uint64(binary.BigEndian.Uint32(b[12:16]))
		durationUnits = uint64(binary.BigEndian.Uint32(b[16:20]))
	}
	p := audioProbe{Codec: "m4a", SampleRate: int(timescale)}
	if timescale > 0 {
		p.DurationMs = int64(durationUnits) * 1000 / int64(timescale)
	}
	return p
}

// findBox scans MP4 boxes in [start,end) for the given 4-char type, returning
// its absolute offset and size. Handles 64-bit (size==1) extended sizes.
func findBox(r io.ReaderAt, start, end int64, want string) (int64, int64, bool) {
	pos := start
	hdr := make([]byte, 16)
	for pos+8 <= end {
		n, _ := r.ReadAt(hdr[:8], pos)
		if n < 8 {
			return 0, 0, false
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		boxStart := pos
		hdrLen := int64(8)
		switch size {
		case 1: // 64-bit size follows
			if _, err := r.ReadAt(hdr[8:16], pos+8); err != nil {
				return 0, 0, false
			}
			size = int64(binary.BigEndian.Uint64(hdr[8:16]))
			hdrLen = 16
		case 0: // extends to end of file
			size = end - pos
		}
		if size < hdrLen {
			return 0, 0, false
		}
		if typ == want {
			return boxStart, size, true
		}
		pos += size
	}
	return 0, 0, false
}
