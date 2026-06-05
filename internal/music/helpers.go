package music

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// musicID mints a short prefixed id, e.g. "trk_9f3a…", "pl_4c1b…".
func musicID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func badReq(c *gin.Context, msg string)   { c.JSON(http.StatusBadRequest, gin.H{"error": msg}) }
func serverErr(c *gin.Context, msg string) { c.JSON(http.StatusInternalServerError, gin.H{"error": msg}) }
func notFound(c *gin.Context, msg string) { c.JSON(http.StatusNotFound, gin.H{"error": msg}) }

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
