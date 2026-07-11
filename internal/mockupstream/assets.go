package mockupstream

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	_ "embed"
)

// assets.go serves built-in placeholder media from /__assets/. Both the sync
// (§7) and async (§8) image/video flows return URLs that point here, so the
// gateway's download + OSS/RustFS transfer link can be exercised end-to-end
// (doc §3, §8.4).
//
// 三种资产都是**真实可解码**的数据：
//   - 图片：启动时用 image/png 生成 512×512 渐变测试图（确定性，无随机）
//   - 音频：启动时生成 440Hz 正弦波 WAV（2 秒、16-bit、24kHz 单声道），可播放
//   - 视频：go:embed 嵌入的 Big Buck Bunny 10 秒 H.264 片段（CC-BY，
//     test-videos.co.uk），可播放
//
// 设置 MOCK_ASSETS_DIR 后，目录下的同名文件（mock-image.png / mock-video.mp4 /
// mock-audio.wav）优先于内置资产，便于随时替换素材而无需重新编译。

// embeddedMP4 is a real, playable H.264 clip (Big Buck Bunny, 10s/360p, CC-BY
// via test-videos.co.uk) so download/transcode/preview links get true video data.
//
//go:embed assets/mock-video.mp4
var embeddedMP4 []byte

// buildPNG renders a deterministic 512×512 test-card PNG: a two-axis color
// gradient with a centered contrast grid, so downstream previews/thumbnails
// show an obviously-synthetic but genuinely decodable picture.
func buildPNG() []byte {
	const size = 512
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.RGBA{
				R: uint8(x * 255 / size),
				G: uint8(y * 255 / size),
				B: uint8(255 - (x+y)*255/(2*size)),
				A: 255,
			}
			// 64px 网格线，便于肉眼确认图片被完整传输/解码。
			if x%64 == 0 || y%64 == 0 {
				c = color.RGBA{R: 32, G: 32, B: 32, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic("mockupstream: cannot encode built-in png: " + err.Error())
	}
	return buf.Bytes()
}

// buildWAV synthesizes a playable 440Hz sine tone: 2s, mono, 16-bit PCM at
// 24kHz (~96KB). Deterministic — no randomness, identical bytes every run.
func buildWAV() []byte {
	const (
		rate    = 24000
		seconds = 2
		freq    = 440.0
	)
	n := rate * seconds
	dataLen := n * 2 // 16-bit mono
	buf := make([]byte, 0, 44+dataLen)

	le16 := func(v int) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, uint16(v)); return b }
	le32 := func(v int) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, uint32(v)); return b }

	buf = append(buf, "RIFF"...)
	buf = append(buf, le32(36+dataLen)...)
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	buf = append(buf, le32(16)...)     // fmt chunk size
	buf = append(buf, le16(1)...)      // PCM
	buf = append(buf, le16(1)...)      // mono
	buf = append(buf, le32(rate)...)   // sample rate
	buf = append(buf, le32(rate*2)...) // byte rate
	buf = append(buf, le16(2)...)      // block align
	buf = append(buf, le16(16)...)     // bits per sample
	buf = append(buf, "data"...)
	buf = append(buf, le32(dataLen)...)
	for i := 0; i < n; i++ {
		v := int16(0.3 * math.MaxInt16 * math.Sin(2*math.Pi*freq*float64(i)/rate))
		buf = append(buf, le16(int(v))...)
	}
	return buf
}

// assetStore holds the resolved media bytes (built-in or MOCK_ASSETS_DIR
// override) plus precomputed base64 forms for the b64_json / inlineData flows.
// Everything is read-only after construction, so handlers can write the shared
// slices straight to the socket.
type assetStore struct {
	png, mp4, wav          []byte
	pngB64, mp4B64, wavB64 []byte
}

// newAssetStore builds the store: built-in defaults, then per-file overrides
// from dir (empty dir = built-ins only). A present-but-unreadable override is
// a fatal misconfiguration, matching the config-file philosophy.
func newAssetStore(dir string) *assetStore {
	st := &assetStore{png: buildPNG(), mp4: embeddedMP4, wav: buildWAV()}
	if dir != "" {
		load := func(name string, dst *[]byte) {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return // no override, keep built-in
				}
				panic("mockupstream: cannot read asset override " + path + ": " + err.Error())
			}
			*dst = data
			Logf("asset override: %s (%d bytes)", path, len(data))
		}
		load("mock-image.png", &st.png)
		load("mock-video.mp4", &st.mp4)
		load("mock-audio.wav", &st.wav)
	}
	enc := func(b []byte) []byte {
		out := make([]byte, base64.StdEncoding.EncodedLen(len(b)))
		base64.StdEncoding.Encode(out, b)
		return out
	}
	st.pngB64, st.mp4B64, st.wavB64 = enc(st.png), enc(st.mp4), enc(st.wav)
	return st
}

// handleAssets serves /__assets/{name}. Unknown names 404.
func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/__assets/")
	var data []byte
	var ctype string
	switch {
	case strings.HasSuffix(name, ".png"):
		data, ctype = s.assets.png, "image/png"
	case strings.HasSuffix(name, ".mp4"):
		data, ctype = s.assets.mp4, "video/mp4"
	case strings.HasSuffix(name, ".wav"):
		data, ctype = s.assets.wav, "audio/wav"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", itoa(len(data)))
	_, _ = w.Write(data)
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
