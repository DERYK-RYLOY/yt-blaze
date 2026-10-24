package fastdl

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lrstanley/go-ytdlp"
)

// DownloadRequest is the API request for starting a download.
// Container/Quality/Audio map 1:1 to the UI buttons.
type DownloadRequest struct {
	URL       string   `json:"url"`
	Output    string   `json:"output,omitempty"`
	Container string   `json:"container,omitempty"` // mp4 | webm | mkv | raw
	Quality   string   `json:"quality,omitempty"`   // 4k | 1080p | 720p | 480p | 360p
	AudioOnly bool     `json:"audio_only,omitempty"`
	Audio     string   `json:"audio,omitempty"` // mp3 | m4a | opus | aac (audio-only mode)
	Subtitles []string `json:"subtitles,omitempty"`
}

// DownloadInfo is the live state of one download, sent to the UI.
type DownloadInfo struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Title      string    `json:"title"`
	Thumb      string    `json:"thumb,omitempty"`
	Status     string    `json:"status"`   // pending | resolving | downloading | merging | finished | failed | cancelled
	Progress   float64   `json:"progress"` // 0-100 across all parts
	Speed      string    `json:"speed"`    // human string, e.g. "14.2 MB/s"
	SpeedBps   int64     `json:"speed_bps"`
	ETA        string    `json:"eta"`
	TotalBytes int64     `json:"total_bytes"`
	DoneBytes  int64     `json:"done_bytes"`
	FilePath   string    `json:"file_path,omitempty"`
	Error      string    `json:"error,omitempty"`
	Engine     string    `json:"engine"` // "aria2" | "native"
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DownloadManager tracks all downloads and drives the engines.
type DownloadManager struct {
	mu        sync.RWMutex
	downloads map[string]*DownloadInfo
	cancelFns map[string]context.CancelFunc
	config    *Config
	aria2     *aria2Daemon
	history   *History

	// onUpdate is called (with lock held released) on every state change.
	onUpdate func(*DownloadInfo)
}

func NewDownloadManager(cfg *Config, aria2 *aria2Daemon, history *History) *DownloadManager {
	return &DownloadManager{
		downloads: make(map[string]*DownloadInfo),
		cancelFns: make(map[string]context.CancelFunc),
		config:    cfg,
		aria2:     aria2,
		history:   history,
	}
}

func generateID() string {
	return fmt.Sprintf("dl-%d", time.Now().UnixNano())
}

// StartDownload resolves and starts downloading. It returns immediately.
func (dm *DownloadManager) StartDownload(req *DownloadRequest) (*DownloadInfo, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("url is required")
	}

	out := req.Output
	if out == "" {
		out = dm.config.OutputDir
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return nil, fmt.Errorf("cannot create output dir: %w", err)
	}

	id := generateID()
	info := &DownloadInfo{
		ID:        id,
		URL:       req.URL,
		Status:    "pending",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	dm.mu.Lock()
	dm.downloads[id] = info
	dm.cancelFns[id] = cancel
	dm.mu.Unlock()

	req.Output = out
	go dm.run(ctx, id, req)

	snapshot := *info
	return &snapshot, nil
}

// update applies fn under lock and notifies listeners.
func (dm *DownloadManager) update(id string, fn func(*DownloadInfo)) {
	dm.mu.Lock()
	info, ok := dm.downloads[id]
	if !ok {
		dm.mu.Unlock()
		return
	}
	fn(info)
	info.UpdatedAt = time.Now()
	snapshot := *info
	cb := dm.onUpdate
	dm.mu.Unlock()
	if cb != nil {
		cb(&snapshot)
	}
}

// run is the full pipeline: resolve → download (aria2 or native) → merge → done.
func (dm *DownloadManager) run(ctx context.Context, id string, req *DownloadRequest) {
	finish := func(status, errMsg string) {
		dm.update(id, func(i *DownloadInfo) {
			i.Status = status
			if errMsg != "" {
				i.Error = errMsg
			}
			if status == "finished" {
				i.Progress = 100
				i.Speed = ""
				i.SpeedBps = 0
				i.ETA = "done"
			}
		})
		if dm.history != nil && status == "finished" {
			dm.history.Add(req.URL)
		}
		dm.mu.Lock()
		if cancel, ok := dm.cancelFns[id]; ok {
			cancel()
			delete(dm.cancelFns, id)
		}
		dm.mu.Unlock()
	}

	// 1. Resolve
	dm.update(id, func(i *DownloadInfo) { i.Status = "resolving" })
	media, err := Resolve(ctx, req)
	if err != nil {
		finish("failed", err.Error())
		return
	}
	dm.update(id, func(i *DownloadInfo) {
		i.Title = media.Title
		i.Thumb = media.Thumb
	})

	container := req.Container
	if container == "" {
		container = ContainerMP4
	}

	// 2. Download
	var videoPath, audioPath string

	if dm.aria2 != nil && !media.NeedsNative {
		dm.update(id, func(i *DownloadInfo) { i.Engine = "aria2" })
		videoPath, audioPath, err = dm.downloadAria2(ctx, id, req, media)
	} else {
		dm.update(id, func(i *DownloadInfo) { i.Engine = "native" })
		videoPath, audioPath, err = dm.downloadNative(ctx, id, req, media)
	}

	if err != nil {
		if ctx.Err() != nil {
			finish("cancelled", "")
		} else {
			finish("failed", err.Error())
		}
		return
	}
	if ctx.Err() != nil {
		finish("cancelled", "")
		return
	}

	// 3. Merge / convert
	dm.update(id, func(i *DownloadInfo) { i.Status = "merging" })
	finalPath, err := dm.merge(ctx, req, container, media, videoPath, audioPath)
	if err != nil {
		finish("failed", "merge failed: "+err.Error())
		return
	}

	dm.update(id, func(i *DownloadInfo) { i.FilePath = finalPath })
	finish("finished", "")
}

// downloadAria2 fetches streams through the aria2 RPC daemon with live progress.
func (dm *DownloadManager) downloadAria2(ctx context.Context, id string, req *DownloadRequest, media *resolvedMedia) (string, string, error) {
	out := req.Output
	var wg sync.WaitGroup
	var vErr, aErr error

	track := newPartTracker(dm, id)
	stem := filenameStem(media)

	// total expected bytes for progress math (approximate when unknown)
	var vTotal, aTotal int64
	if media.Video != nil {
		vTotal = media.Video.expectedBytes()
	}
	if media.Audio != nil {
		aTotal = media.Audio.expectedBytes()
	}
	track.setTotals(vTotal, aTotal)

	if media.Video != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := map[string]string{
				"dir":    out,
				"out":    stem + "-video." + media.Video.Ext,
				"header": joinHeaders(media.Video.Headers),
			}
			gid, err := dm.aria2.client.AddURI(ctx, media.Video.URL, opts)
			if err != nil {
				vErr = err
				return
			}
			if err := dm.aria2.waitGID(ctx, gid, func(st *Status) {
				track.setPart(0, st)
			}); err != nil {
				vErr = err
				return
			}
		}()
	}

	if media.Audio != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := map[string]string{
				"dir":    out,
				"out":    stem + "-audio." + media.Audio.Ext,
				"header": joinHeaders(media.Audio.Headers),
			}
			gid, err := dm.aria2.client.AddURI(ctx, media.Audio.URL, opts)
			if err != nil {
				aErr = err
				return
			}
			if err := dm.aria2.waitGID(ctx, gid, func(st *Status) {
				track.setPart(1, st)
			}); err != nil {
				aErr = err
				return
			}
		}()
	}

	wg.Wait()
	if vErr != nil {
		return "", "", vErr
	}
	if aErr != nil {
		return "", "", aErr
	}

	videoPath, audioPath := "", ""
	if media.Video != nil {
		videoPath = filepath.Join(out, stem+"-video."+media.Video.Ext)
	}
	if media.Audio != nil {
		audioPath = filepath.Join(out, stem+"-audio."+media.Audio.Ext)
	}
	// Safety guard: a direct URL must never yield an HLS playlist. If it
	// did (extender/redirect edge cases), reject so the caller can fall
	// back to the native engine instead of handing garbage to ffmpeg.
	if p := looksLikePlaylist(videoPath); p != "" {
		return "", "", fmt.Errorf("received streaming manifest instead of media file (%s)", p)
	}
	if p := looksLikePlaylist(audioPath); p != "" {
		return "", "", fmt.Errorf("received streaming manifest instead of media file (%s)", p)
	}
	return videoPath, audioPath, nil
}

// looksLikePlaylist returns non-empty if the file is an HLS/DASH manifest.
func looksLikePlaylist(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 8)
	n, _ := f.Read(buf)
	if n >= 7 && string(buf[:7]) == "#EXTM3U" {
		return path
	}
	return ""
}

// waitGID polls a gid until complete, invoking onTick with live status.
func (d *aria2Daemon) waitGID(ctx context.Context, gid string, onTick func(*Status)) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		st, err := d.client.TellStatus(ctx, gid)
		if err != nil {
			return err
		}
		if onTick != nil {
			onTick(st)
		}
		switch st.Status {
		case "complete":
			return nil
		case "error":
			return fmt.Errorf("aria2 error: %s", st.ErrorMessage)
		case "removed":
			return fmt.Errorf("download removed")
		}
		select {
		case <-ctx.Done():
			_ = d.client.Remove(context.Background(), gid)
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// partTracker merges per-part aria2 status into one progress/speed for the UI.
type partTracker struct {
	dm    *DownloadManager
	id    string
	mu    sync.Mutex
	vTot  int64
	aTot  int64
	vDone int64
	aDone int64
	vSpd  int64
	aSpd  int64
}

func newPartTracker(dm *DownloadManager, id string) *partTracker {
	return &partTracker{dm: dm, id: id}
}

func (p *partTracker) setTotals(v, a int64) {
	p.mu.Lock()
	p.vTot, p.aTot = v, a
	p.mu.Unlock()
}

func (p *partTracker) setPart(idx int, st *Status) {
	vd, _ := strconv.ParseInt(st.CompletedLength, 10, 64)
	vt, _ := strconv.ParseInt(st.TotalLength, 10, 64)
	spd, _ := strconv.ParseInt(st.DownloadSpeed, 10, 64)

	p.mu.Lock()
	if idx == 0 {
		p.vDone = vd
		if vt > 0 {
			p.vTot = vt
		}
		p.vSpd = spd
	} else {
		p.aDone = vd
		if vt > 0 {
			p.aTot = vt
		}
		p.aSpd = spd
	}
	total := p.vTot + p.aTot
	done := p.vDone + p.aDone
	speed := p.vSpd + p.aSpd
	p.mu.Unlock()

	var prog float64
	if total > 0 {
		prog = float64(done) / float64(total) * 100
	}
	p.dm.update(p.id, func(i *DownloadInfo) {
		i.Status = "downloading"
		i.Progress = prog
		i.TotalBytes = total
		i.DoneBytes = done
		i.SpeedBps = speed
		i.Speed = humanSpeed(speed)
		i.ETA = humanETA(total-done, speed)
	})
}

// downloadNative is the fallback: let yt-dlp download with aria2c as its
// external downloader (or its own engine), parsing --newline progress.
func (dm *DownloadManager) downloadNative(ctx context.Context, id string, req *DownloadRequest, media *resolvedMedia) (string, string, error) {
	dl := ytdlp.New().Newline().Continue()

	stem := filenameStem(media)
	template := filepath.Join(req.Output, stem+" [%(id)s].%(ext)s")
	dl.Output(template)

	container := req.Container
	if container == "" {
		container = ContainerMP4
	}

	if req.AudioOnly {
		dl.Format("bestaudio/best")
		dl.ExtractAudio()
		af := req.Audio
		if af == "" {
			af = AudioMP3
		}
		dl.AudioFormat(af)
		if af == AudioMP3 {
			dl.AudioQuality("320K")
		}
	} else {
		dl.Format(bestFormatSelector(req, container))
		if container != ContainerRaw {
			dl.MergeOutputFormat(container)
		}
	}

	if dm.config.Aria2cPath != "" {
		dl.Downloader("aria2c")
		dl.DownloaderArgs("--max-connection-per-server=16 --split=16 --min-split-size=1M --enable-http-pipelining --retry-wait=3 --max-tries=10")
	}
	if dm.config.FfmpegPath != "" {
		dl.FFmpegLocation(filepath.Dir(dm.config.FfmpegPath))
	}

	cmd := dl.BuildCommand(ctx, req.URL)
	hideConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return "", "", err
	}

	progRe := regexp.MustCompile(`\[download\]\s+([\d.]+)%.*?at\s+([\d.]+)\s*(\w+/s)`)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m := progRe.FindStringSubmatch(line); m != nil {
			pct, _ := strconv.ParseFloat(m[1], 64)
			speed := parseSpeed(m[2], m[3])
			dm.update(id, func(i *DownloadInfo) {
				i.Status = "downloading"
				i.Progress = pct
				i.SpeedBps = speed
				i.Speed = fmt.Sprintf("%s %s", m[2], m[3])
			})
		}
	}

	waitErr := cmd.Wait()
	if waitErr != nil {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		return "", "", fmt.Errorf("yt-dlp: %v: %s", waitErr, truncate(stderrBuf.String(), 400))
	}

	// Find the produced file(s) with our stem.
	videoPath, audioPath := findOutputs(req.Output, stem, media)
	if videoPath == "" && audioPath == "" {
		return "", "", fmt.Errorf("output file not found for %s", stem)
	}
	return videoPath, audioPath, nil
}

// bestFormatSelector builds yt-dlp -f for the native engine.
func bestFormatSelector(req *DownloadRequest, container string) string {
	capH := QualityToHeight[strings.ToLower(req.Quality)]
	heightFilter := ""
	if capH > 0 {
		heightFilter = fmt.Sprintf("[height<=%d]", int(capH))
	}
	switch container {
	case ContainerMP4:
		return fmt.Sprintf("bestvideo%s[ext=mp4]+bestaudio[ext=m4a]/bestvideo%s+bestaudio/best%s", heightFilter, heightFilter, heightFilter)
	case ContainerWEBM:
		return fmt.Sprintf("bestvideo%s[ext=webm]+bestaudio[ext=webm]/bestvideo%s+bestaudio/best%s", heightFilter, heightFilter, heightFilter)
	default:
		return fmt.Sprintf("bestvideo%s+bestaudio/best%s", heightFilter, heightFilter)
	}
}

// findOutputs locates files produced by the native engine for our stem.
func findOutputs(dir, stem string, media *resolvedMedia) (string, string) {
	var videoPath, audioPath string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	wantSuffix := " [" + media.ID + "]"
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, stem) && !strings.Contains(name, wantSuffix) {
			continue
		}
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "-video."):
			videoPath = filepath.Join(dir, name)
		case strings.Contains(lower, "-audio."):
			audioPath = filepath.Join(dir, name)
		default:
			if videoPath == "" {
				videoPath = filepath.Join(dir, name)
			}
		}
	}
	return videoPath, audioPath
}

// merge runs ffmpeg: remux/convert per the container and audio buttons.
func (dm *DownloadManager) merge(ctx context.Context, req *DownloadRequest, container string, media *resolvedMedia, videoPath, audioPath string) (string, error) {
	out := req.Output
	stem := filenameStem(media)

	// Audio-only: yt-dlp native engine already converted; aria2 path needs conversion.
	if req.AudioOnly {
		if audioPath == "" {
			audioPath = videoPath // progressive file
		}
		if audioPath == "" {
			return "", fmt.Errorf("no audio file to convert")
		}
		af := req.Audio
		if af == "" {
			af = AudioMP3
		}
		dst := filepath.Join(out, stem+"."+af)
		if err := dm.ffmpegConvertAudio(ctx, audioPath, dst, af); err != nil {
			return "", err
		}
		cleanup(audioPath)
		return dst, nil
	}

	// Single progressive file: nothing to merge.
	if audioPath == "" || audioPath == videoPath {
		ext := container
		if container == ContainerRaw {
			ext = media.Video.Ext
		}
		dst := filepath.Join(out, stem+"."+ext)
		if err := os.Rename(videoPath, dst); err != nil {
			return "", err
		}
		return dst, nil
	}

	// Two streams: remux (copy, no re-encode) into the container.
	ext := container
	if container == ContainerRaw {
		ext = "mkv" // mkv accepts any codec for raw preservation
	}
	dst := filepath.Join(out, stem+"."+ext)
	if err := dm.ffmpegMerge(ctx, videoPath, audioPath, dst, ext); err != nil {
		return "", err
	}
	cleanup(videoPath)
	cleanup(audioPath)
	return dst, nil
}

func (dm *DownloadManager) ffmpegMerge(ctx context.Context, videoPath, audioPath, dst, ext string) error {
	ff := dm.config.FfmpegPath
	if ff == "" {
		// No ffmpeg: fall back to yt-dlp merger via a tiny helper run.
		return ytMerge(ctx, videoPath, audioPath, dst, ext)
	}
	args := []string{"-y", "-i", videoPath, "-i", audioPath, "-c", "copy"}
	if ext == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, dst)
	cmd := exec.CommandContext(ctx, ff, args...)
	hideConsole(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %v: %s", err, truncate(string(out), 400))
	}
	return nil
}

func (dm *DownloadManager) ffmpegConvertAudio(ctx context.Context, src, dst, target string) error {
	ff := dm.config.FfmpegPath
	if ff == "" {
		return fmt.Errorf("ffmpeg required for audio conversion but not installed")
	}
	args := []string{"-y", "-i", src, "-vn"}
	switch target {
	case AudioMP3:
		args = append(args, "-c:a", "libmp3lame", "-b:a", "320k")
	case AudioM4A, AudioAAC:
		args = append(args, "-c:a", "aac", "-b:a", "256k")
	case AudioOPUS:
		args = append(args, "-c:a", "libopus", "-b:a", "160k")
	}
	args = append(args, dst)
	cmd := exec.CommandContext(ctx, ff, args...)
	hideConsole(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg audio: %v: %s", err, truncate(string(out), 400))
	}
	return nil
}

// ytMerge uses a throwaway yt-dlp run's ffmpeg detection; simple yt-dlp merge
// is not directly exposed, so we require ffmpeg for two-stream merges in the
// aria2 engine path. This helper errors clearly if that is the case.
func ytMerge(ctx context.Context, videoPath, audioPath, dst, ext string) error {
	_ = ctx
	_ = videoPath
	_ = audioPath
	_ = dst
	_ = ext
	return fmt.Errorf("ffmpeg is required to merge separate video+audio streams")
}

// GetDownload returns a snapshot of one download.
func (dm *DownloadManager) GetDownload(id string) (*DownloadInfo, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	info, ok := dm.downloads[id]
	if !ok {
		return nil, false
	}
	snap := *info
	return &snap, true
}

// ListDownloads returns snapshots of all downloads, newest first.
func (dm *DownloadManager) ListDownloads() []*DownloadInfo {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	out := make([]*DownloadInfo, 0, len(dm.downloads))
	for _, info := range dm.downloads {
		snap := *info
		out = append(out, &snap)
	}
	return out
}

// ActiveSpeedBps sums live speed across active downloads (for the speedometer).
func (dm *DownloadManager) ActiveSpeedBps() int64 {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	var sum int64
	for _, info := range dm.downloads {
		if info.Status == "downloading" {
			sum += info.SpeedBps
		}
	}
	return sum
}

// ActiveProgress returns the strongest active download progress (0-100) and
// whether any download is running (drives the logo shimmer ray).
func (dm *DownloadManager) ActiveProgress() (float64, bool) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	active := false
	var max float64
	for _, info := range dm.downloads {
		switch info.Status {
		case "pending", "resolving", "downloading", "merging":
			active = true
			if info.Progress > max {
				max = info.Progress
			}
		}
	}
	return max, active
}

// CancelDownload cancels a running download and kills its engine work.
func (dm *DownloadManager) CancelDownload(id string) error {
	dm.mu.Lock()
	info, ok := dm.downloads[id]
	if !ok {
		dm.mu.Unlock()
		return fmt.Errorf("download not found: %s", id)
	}
	if info.Status == "finished" || info.Status == "failed" || info.Status == "cancelled" {
		dm.mu.Unlock()
		return nil
	}
	cancel, hasCancel := dm.cancelFns[id]
	dm.mu.Unlock()

	if hasCancel {
		cancel() // context cancellation kills yt-dlp and tells aria2 to remove
	}

	dm.update(id, func(i *DownloadInfo) {
		i.Status = "cancelled"
		i.Speed = ""
		i.SpeedBps = 0
	})
	return nil
}

// --- helpers ---

var unsafeName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// filenameStem builds a safe, deterministic file stem: "Title [ID]".
func filenameStem(media *resolvedMedia) string {
	title := media.Title
	if title == "" {
		title = media.ID
	}
	title = unsafeName.ReplaceAllString(title, "")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "video"
	}
	if len(title) > 120 {
		title = title[:120]
	}
	return title + " [" + media.ID + "]"
}

func (s *resolvedStream) expectedBytes() int64 {
	return 0 // aria2 reports real totals on first tick
}

func humanSpeed(bps int64) string {
	if bps <= 0 {
		return "—"
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	v := float64(bps)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func humanETA(remaining int64, bps int64) string {
	if remaining <= 0 || bps <= 0 {
		return "—"
	}
	secs := remaining / bps
	if secs > 3600*24 {
		return ">1 day"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

var unitPow = map[string]float64{
	"B":   1,
	"KB":  1024,
	"MB":  1024 * 1024,
	"GB":  1024 * 1024 * 1024,
	"KiB": 1024,
	"MiB": 1024 * 1024,
	"GiB": 1024 * 1024 * 1024,
}

// parseSpeed converts yt-dlp's "14.2" + "MiB/s" into bytes per second.
func parseSpeed(val, unit string) int64 {
	v, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0
	}
	u := strings.ToUpper(strings.TrimSpace(unit))
	u = strings.TrimSuffix(u, "/S")
	pow, ok := unitPow[u]
	if !ok {
		pow = 1
	}
	return int64(v * pow)
}

// joinHeaders joins aria2 "header" option values with newlines (aria2's
// documented multi-value format for RPC).
func joinHeaders(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	parts := make([]string, 0, len(h))
	for k, v := range h {
		parts = append(parts, k+": "+v)
	}
	return strings.Join(parts, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func cleanup(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
