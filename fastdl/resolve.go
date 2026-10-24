package fastdl

import (
	"context"
	"fmt"
	"strings"

	"github.com/lrstanley/go-ytdlp"
)

// Container targets matching the UI buttons.
const (
	ContainerMP4  = "mp4"
	ContainerWEBM = "webm"
	ContainerMKV  = "mkv"
	ContainerRaw  = "raw"
)

// Audio targets matching the UI buttons.
const (
	AudioMP3  = "mp3"
	AudioM4A  = "m4a"
	AudioOPUS = "opus"
	AudioAAC  = "aac"
)

// QualityToHeight maps UI quality buttons to max video height.
var QualityToHeight = map[string]float64{
	"4k":    2160,
	"1080p": 1080,
	"720p":  720,
	"480p":  480,
	"360p":  360,
}

// resolvedStream is one chosen media stream (video-only or audio-only).
type resolvedStream struct {
	URL     string
	Ext     string
	Format  string // format id, for filenames
	Headers map[string]string
}

// resolvedMedia is the result of resolving one URL.
type resolvedMedia struct {
	Title    string
	ID       string
	Thumb    string
	Video    *resolvedStream // nil in audio-only mode
	Audio    *resolvedStream
	Separate bool // true when video+audio must be merged
	// NeedsNative forces the yt-dlp native engine: the only available
	// formats are HLS/DASH manifests that aria2 cannot fetch directly.
	NeedsNative bool
}

// isDirectHTTP reports whether a format URL is a plain downloadable file
// (http/https direct) rather than a streaming manifest (m3u8/DASH).
// aria2 must only ever receive direct URLs — a manifest would be saved
// as a tiny useless text file.
func isDirectHTTP(f *ytdlp.ExtractedFormat) bool {
	p := strings.ToLower(strOf(f.Protocol))
	switch p {
	case "", "http", "https":
		return true
	default:
		// m3u8_native, m3u8, http_dash_segments, etc.
		return false
	}
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// pickVideo selects the best video-only stream: height <= cap, codec preferred
// for the container, highest resolution then bitrate.
func pickVideo(formats []*ytdlp.ExtractedFormat, capHeight float64, container string) *ytdlp.ExtractedFormat {
	var best *ytdlp.ExtractedFormat
	var bestScore float64
	for _, f := range formats {
		if f == nil || f.URL == "" || !isDirectHTTP(f) {
			continue
		}
		if strOf(f.ACodec) != "none" && strOf(f.ACodec) != "" {
			continue // combined formats are handled by native fallback only
		}
		if strOf(f.VCodec) == "none" || strOf(f.VCodec) == "" {
			continue
		}
		h := 0.0
		if f.Height != nil {
			h = *f.Height
		}
		if capHeight > 0 && h > capHeight {
			continue
		}
		score := h*100000 + fbr(f)
		// codec preference by container
		vc := strings.ToLower(strOf(f.VCodec))
		ext := strings.ToLower(strOf(f.Extension))
		switch container {
		case ContainerMP4:
			if strings.HasPrefix(vc, "avc") && ext == "mp4" {
				score += 5e8
			}
		case ContainerWEBM:
			if strings.HasPrefix(vc, "vp9") || strings.HasPrefix(vc, "vp09") {
				score += 5e8
			} else if strings.HasPrefix(vc, "av01") || strings.HasPrefix(vc, "av1") {
				score += 4e8
			}
		}
		if best == nil || score > bestScore {
			best, bestScore = f, score
		}
	}
	return best
}

// pickAudio selects the best audio-only stream.
func pickAudio(formats []*ytdlp.ExtractedFormat, container string) *ytdlp.ExtractedFormat {
	var best *ytdlp.ExtractedFormat
	var bestScore float64
	for _, f := range formats {
		if f == nil || f.URL == "" || !isDirectHTTP(f) {
			continue
		}
		if strOf(f.VCodec) != "none" && strOf(f.VCodec) != "" {
			continue
		}
		ac := strings.ToLower(strOf(f.ACodec))
		if ac == "none" || ac == "" {
			continue
		}
		score := fbr(f)
		ext := strings.ToLower(strOf(f.Extension))
		switch container {
		case ContainerMP4:
			if strings.HasPrefix(ac, "mp4a") || ext == "m4a" {
				score += 1e8
			}
		case ContainerWEBM:
			if strings.HasPrefix(ac, "opus") {
				score += 1e8
			}
		}
		if best == nil || score > bestScore {
			best, bestScore = f, score
		}
	}
	return best
}

func fbr(f *ytdlp.ExtractedFormat) float64 {
	if f.TBR != nil {
		return *f.TBR
	}
	if f.VBR != nil {
		return *f.VBR
	}
	if f.ABR != nil {
		return *f.ABR
	}
	return 0
}

// Resolve runs yt-dlp metadata extraction and picks the streams per request.
func Resolve(ctx context.Context, req *DownloadRequest) (*resolvedMedia, error) {
	ny := ytdlp.New().NoPlaylist().SkipDownload()

	infos, _, err := ny.ExtractInfo(ctx, req.URL)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp resolve failed: %w", err)
	}
	if len(infos) == 0 || infos[0] == nil {
		return nil, fmt.Errorf("no metadata returned for url")
	}
	info := infos[0]

	out := &resolvedMedia{
		Title: strOf(info.Title),
		ID:    info.ID,
	}
	if len(info.Thumbnails) > 0 {
		out.Thumb = info.Thumbnails[len(info.Thumbnails)-1].URL
	}

	container := req.Container
	if container == "" {
		container = ContainerMP4
	}

	capH := QualityToHeight[strings.ToLower(req.Quality)]

	if req.AudioOnly {
		af := pickAudio(info.Formats, container)
		if af == nil {
			// Some sites only expose combined formats; fall back to the top one.
			af = pickCombined(info.Formats)
		}
		if af == nil {
			return nil, fmt.Errorf("no audio format available")
		}
		out.Audio = streamOf(af)
		return out, nil
	}

	v := pickVideo(info.Formats, capH, container)
	a := pickAudio(info.Formats, container)

	if v != nil && a != nil {
		out.Video = streamOf(v)
		out.Audio = streamOf(a)
		out.Separate = true
		return out, nil
	}

	// No separate direct streams — try a combined progressive format.
	combined := pickCombinedCapped(info.Formats, capH)
	if combined != nil {
		out.Video = streamOf(combined)
		return out, nil
	}

	// Everything available is HLS/DASH (common for higher qualities on
	// YouTube). aria2 cannot fetch manifests — hand off to the native
	// yt-dlp engine which downloads segments and merges itself.
	out.NeedsNative = true
	return out, nil
}

func pickCombined(formats []*ytdlp.ExtractedFormat) *ytdlp.ExtractedFormat {
	return pickCombinedCapped(formats, 0)
}

func pickCombinedCapped(formats []*ytdlp.ExtractedFormat, capHeight float64) *ytdlp.ExtractedFormat {
	var best *ytdlp.ExtractedFormat
	var bestScore float64
	for _, f := range formats {
		if f == nil || f.URL == "" || !isDirectHTTP(f) {
			continue
		}
		vc := strOf(f.VCodec)
		ac := strOf(f.ACodec)
		if (vc == "none" || vc == "") && (ac == "none" || ac == "") {
			continue
		}
		// combined = has both real video and real audio, or at least real audio
		if !(vc != "none" && vc != "" && ac != "none" && ac != "") &&
			!(ac != "none" && ac != "") {
			continue
		}
		h := 0.0
		if f.Height != nil {
			h = *f.Height
		}
		if capHeight > 0 && h > capHeight {
			continue
		}
		score := h*100000 + fbr(f)
		if best == nil || score > bestScore {
			best, bestScore = f, score
		}
	}
	return best
}

func streamOf(f *ytdlp.ExtractedFormat) *resolvedStream {
	s := &resolvedStream{
		URL:     f.URL,
		Ext:     strOf(f.Extension),
		Format:  strOf(f.FormatID),
		Headers: f.HTTPHeaders,
	}
	if s.Ext == "" {
		s.Ext = "mp4"
	}
	return s
}
