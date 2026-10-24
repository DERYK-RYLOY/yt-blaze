package fastdl

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lrstanley/go-ytdlp"
)

// EstimateRequest asks the backend to resolve a URL and compute the final
// download size for every quality / audio-only variant the UI can pick.
type EstimateRequest struct {
	URL string `json:"url"`
}

// EstimateResponse holds one size (bytes) per UI selection key so the
// frontend can flip between variants instantly without re-querying.
type EstimateResponse struct {
	Title    string                      `json:"title"`
	Duration float64                     `json:"duration_s"`
	Video    map[string]map[string]int64 `json:"video"` // container -> quality -> bytes
	Audio    map[string]int64            `json:"audio"` // mp3/m4a/opus/aac -> bytes
}

// Audio target bitrate in kbit/s: mirrors the -b:a flags in
// ffmpegConvertAudio, so audio-only sizes are accurate for the CONVERTED
// file the user actually ends up with.
var audioBitrate = map[string]float64{
	AudioMP3:  320,
	AudioM4A:  256,
	AudioAAC:  256,
	AudioOPUS: 160,
}

// estimate state: serializes resolver runs (yt-dlp metadata is 1-3s) and
// keeps the last successful result per URL for instant re-answers.
type estimator struct {
	mu       sync.Mutex
	lastKey  string
	lastResp *EstimateResponse
	lastAt   time.Time
}

func (b *Backend) handleEstimate(c *gin.Context) {
	var req EstimateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	url := strings.TrimSpace(req.URL)
	if len(url) < 8 || !strings.Contains(url, "://") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid url"})
		return
	}

	// serialize: one yt-dlp resolve at a time
	b.est.mu.Lock()
	defer b.est.mu.Unlock()

	if b.est.lastKey == url && time.Since(b.est.lastAt) < 5*time.Minute && b.est.lastResp != nil {
		c.JSON(http.StatusOK, b.est.lastResp)
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()

	resp, err := buildEstimate(ctx, url)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	b.est.lastKey, b.est.lastResp, b.est.lastAt = url, resp, time.Now()
	c.JSON(http.StatusOK, resp)
}

func buildEstimate(ctx context.Context, url string) (*EstimateResponse, error) {
	ny := ytdlp.New().NoPlaylist().SkipDownload()
	infos, _, err := ny.ExtractInfo(ctx, url)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 || infos[0] == nil {
		return nil, fmt.Errorf("no metadata returned for url")
	}
	info := infos[0]

	dur := 0.0
	if info.Duration != nil {
		dur = *info.Duration
	}

	out := &EstimateResponse{
		Title:    strOf(info.Title),
		Duration: dur,
		Video:    map[string]map[string]int64{},
		Audio:    map[string]int64{},
	}

	// bytes for one format: exact fs > approx fs > bitrate*duration
	fmtBytes := func(f *ytdlp.ExtractedFormat) int64 {
		if f == nil {
			return 0
		}
		if f.FileSize != nil && *f.FileSize > 0 {
			return int64(*f.FileSize)
		}
		if f.FileSizeApprox != nil && *f.FileSizeApprox > 0 {
			return int64(*f.FileSizeApprox)
		}
		if br := fbr(f); br > 0 {
			return kbpsToBytes(br, dur)
		}
		return 0
	}

	videoSize := func(capH float64, container string) int64 {
		v := pickVideo(info.Formats, capH, container)
		a := pickAudio(info.Formats, container)
		if v != nil {
			return fmtBytes(v) + fmtBytes(a)
		}
		cb := pickCombinedCapped(info.Formats, capH)
		if cb != nil {
			return fmtBytes(cb)
		}
		// manifest-only (HLS/DASH): bitrate estimate for the best stream at
		// or below the cap
		bestBr := 0.0
		for _, f := range info.Formats {
			if f == nil || f.URL == "" {
				continue
			}
			h := 0.0
			if f.Height != nil {
				h = *f.Height
			}
			if capH > 0 && h > capH {
				continue
			}
			if fbr(f) >= bestBr {
				bestBr = fbr(f)
			}
		}
		if bestBr > 0 {
			return kbpsToBytes(bestBr, dur)
		}
		return 0
	}

	allContainers := []string{ContainerMP4, ContainerWEBM, ContainerMKV, ContainerRaw}
	for _, c := range allContainers {
		m := map[string]int64{}
		for q, h := range QualityToHeight {
			if s := videoSize(h, c); s > 0 {
				m[q] = s
			}
		}
		out.Video[c] = m
	}
	for af, kbps := range audioBitrate {
		base := kbpsToBytes(kbps, dur)
		if dur > 0 {
			out.Audio[af] = base // converted size is what the user gets
		}
	}
	return out, nil
}

// kbpsToBytes: kbit/s * 1000/8 * seconds
func kbpsToBytes(kbps, seconds float64) int64 {
	if kbps <= 0 || seconds <= 0 {
		return 0
	}
	b := kbps * 1000 / 8 * seconds
	if b < 0 {
		return 0
	}
	return int64(b)
}
