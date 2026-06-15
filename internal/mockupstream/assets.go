package mockupstream

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// assets.go serves built-in placeholder media from /__assets/. Both the sync
// (§7) and async (§8) image/video flows return URLs that point here, so the
// gateway's download + OSS/RustFS transfer link can be exercised end-to-end
// (doc §3, §8.4). Assets are tiny, valid files embedded as base64.

// mockPNGBase64 is a 1x1 transparent PNG (smallest valid PNG).
const mockPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// mockMP4Base64 is a minimal ftyp/moov MP4 container. It is a structurally
// valid (if empty) MP4 sufficient for download/transfer verification.
const mockMP4Base64 = "AAAAHGZ0eXBpc29tAAACAGlzb21pc28yYXZjMQAAAAhmcmVlAAAAGm1kYXQAAAAA" +
	"AAAACG1vb3YAAAAA"

// mockBigB64 is a ~10MB base64 string used for load testing the b64_json image
// flow. The bytes are not a valid image — clients won't decode it to a picture —
// it exists purely to exercise large-payload transfer/bandwidth. base64 字母表
// 里 'A' 是合法字符,这里用纯 'A' 填充到目标长度(长度取 4 的倍数以保证可解码)。
const bigB64Size = 10 * 1024 * 1024 // 10 MiB of base64 text

var mockBigB64 = strings.Repeat("A", bigB64Size-bigB64Size%4)

// mockBigB64Bytes is the same payload as a shared, read-only []byte so the hot
// b64 response path can write it straight to the socket (no string→[]byte copy,
// no JSON escape scan). See writeMediaJSON.
var mockBigB64Bytes = []byte(mockBigB64)

func mustDecode(b64 string) []byte {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		panic("mockupstream: invalid embedded asset: " + err.Error())
	}
	return data
}

var (
	mockPNG = mustDecode(mockPNGBase64)
	mockMP4 = mustDecode(mockMP4Base64)
)

// handleAssets serves /__assets/{name}. Unknown names 404.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/__assets/")
	switch {
	case strings.HasSuffix(name, ".png"):
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", itoa(len(mockPNG)))
		_, _ = w.Write(mockPNG)
	case strings.HasSuffix(name, ".mp4"):
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", itoa(len(mockMP4)))
		_, _ = w.Write(mockMP4)
	default:
		http.NotFound(w, r)
	}
}

// assetURL builds an absolute URL to a built-in asset, derived from the request
// host so the gateway can reach it back regardless of how the mock is addressed.
func (s *Server) assetURL(r *http.Request, name string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xf := r.Header.Get("X-Forwarded-Proto"); xf != "" {
		scheme = xf
	}
	host := r.Host
	if host == "" {
		host = ListenAddr
	}
	return scheme + "://" + host + "/__assets/" + name
}
