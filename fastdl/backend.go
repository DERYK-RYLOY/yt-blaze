package fastdl

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// Backend wires the download manager, history, and HTTP API together.
type Backend struct {
	cfg     *Config
	manager *DownloadManager
	history *History
	aria2   *aria2Daemon
	router  *gin.Engine

	sseMu      sync.Mutex
	sseClients map[chan []byte]struct{}

	est estimator // serializes yt-dlp resolves + caches last result
}

// NewBackend sets up tools (yt-dlp, ffmpeg, aria2) and builds the API.
func NewBackend(cfg *Config) (*Backend, error) {
	b := &Backend{
		cfg:        cfg,
		history:    LoadHistory(cfg.OutputDir),
		sseClients: make(map[chan []byte]struct{}),
	}

	// Setup tools: yt-dlp (auto-install), ffmpeg (auto-install), aria2 (PATH).
	if err := setupTools(cfg); err != nil {
		log.Printf("tool setup warning: %v", err)
	}

	// Start aria2 RPC daemon when the binary is available.
	if cfg.Aria2cPath != "" {
		daemon, err := StartAria2(cfg.Aria2cPath, cfg.Aria2Port, cfg.Aria2Secret)
		if err != nil {
			log.Printf("aria2 daemon warning: %v (will use native engine)", err)
		} else {
			b.aria2 = daemon
		}
	}

	b.manager = NewDownloadManager(cfg, b.aria2, b.history)
	b.manager.onUpdate = b.broadcastProgress

	b.buildRouter()
	return b, nil
}

// Run starts the HTTP server (blocking) on cfg.Addr().
func (b *Backend) Run() error {
	return b.router.Run(b.cfg.Addr())
}

// Serve runs the HTTP server on a pre-bound listener (race-free port
// ownership for the desktop app).
func (b *Backend) Serve(l net.Listener) error {
	return b.router.RunListener(l)
}

// Shutdown stops the aria2 daemon.
func (b *Backend) Shutdown() {
	if b.aria2 != nil {
		_ = b.aria2.Stop()
	}
}

// ProgressEvent is what /api/progress/stream emits — drives the speedometer,
// progress bar, and the logo shimmer ray.
type ProgressEvent struct {
	Active   bool          `json:"active"` // any download running → shimmer ray on
	Progress float64       `json:"progress"`
	Speed    string        `json:"speed"`
	SpeedBps int64         `json:"speed_bps"`
	ETA      string        `json:"eta"`
	Status   string        `json:"status"`
	Title    string        `json:"title,omitempty"`
	Download *DownloadInfo `json:"download,omitempty"`
}

// currentEvent builds the aggregate progress event from the manager.
func (b *Backend) currentEvent() ProgressEvent {
	prog, active := b.manager.ActiveProgress()
	speed := b.manager.ActiveSpeedBps()

	ev := ProgressEvent{
		Active:   active,
		Progress: prog,
		Speed:    humanSpeed(speed),
		SpeedBps: speed,
		ETA:      humanETA(0, speed), // refined per-download below
	}

	// find the most relevant active download for title/eta/detail
	var best *DownloadInfo
	b.manager.mu.RLock()
	for _, d := range b.manager.downloads {
		switch d.Status {
		case "pending", "resolving", "downloading", "merging":
			if best == nil || d.Progress > best.Progress {
				snap := *d
				best = &snap
			}
		}
	}
	b.manager.mu.RUnlock()

	if best != nil {
		ev.Status = best.Status
		ev.Title = best.Title
		ev.ETA = best.ETA
		ev.Download = best
	} else {
		ev.Status = "idle"
		ev.ETA = "—"
	}
	return ev
}

func (b *Backend) broadcastProgress(*DownloadInfo) {
	ev := b.currentEvent()
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.sseMu.Lock()
	for ch := range b.sseClients {
		select {
		case ch <- data:
		default: // drop if client is slow; next tick will refresh
		}
	}
	b.sseMu.Unlock()
}

func (b *Backend) buildRouter() {
	if b.cfg.Host == "127.0.0.1" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())

	// CORS for the webview/dev server.
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	api := r.Group("/api")
	{
		// Health: reports which engines are live (drives the GO/YTDLP badges).
		api.GET("/health", func(c *gin.Context) {
			aria2OK := false
			if b.aria2 != nil {
				aria2OK = true
			}
			c.JSON(http.StatusOK, gin.H{
				"status":  "ok",
				"version": Version,
				"tools": gin.H{
					"ytdlp":  b.cfg.YtdlpOK,
					"ffmpeg": b.cfg.FfmpegPath != "",
					"aria2":  aria2OK,
				},
				"engine": gin.H{
					"aria2":  aria2OK,
					"native": true,
				},
				"output_dir": b.cfg.OutputDir,
			})
		})

		// Size estimates for every quality / audio-only variant (MB badge).
		api.POST("/estimate", b.handleEstimate)

		// Start a download.
		api.POST("/download", func(c *gin.Context) {
			var req DownloadRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			info, err := b.manager.StartDownload(&req)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusAccepted, info)
		})

		// List all downloads.
		api.GET("/downloads", func(c *gin.Context) {
			c.JSON(http.StatusOK, b.manager.ListDownloads())
		})

		// One download.
		api.GET("/downloads/:id", func(c *gin.Context) {
			info, ok := b.manager.GetDownload(c.Param("id"))
			if !ok {
				c.JSON(http.StatusNotFound, gin.H{"error": "download not found"})
				return
			}
			c.JSON(http.StatusOK, info)
		})

		// Cancel.
		api.DELETE("/downloads/:id", func(c *gin.Context) {
			if err := b.manager.CancelDownload(c.Param("id")); err != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "cancelled"})
		})

		// Aggregate progress snapshot (for polling clients).
		api.GET("/progress", func(c *gin.Context) {
			c.JSON(http.StatusOK, b.currentEvent())
		})

		// Live progress stream (SSE) — speedometer, progress bar, shimmer ray.
		api.GET("/progress/stream", func(c *gin.Context) {
			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				c.Status(http.StatusInternalServerError)
				return
			}
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			c.Writer.Header().Set("Cache-Control", "no-cache")
			c.Writer.Header().Set("Connection", "keep-alive")
			c.Writer.Header().Set("X-Accel-Buffering", "no")

			ch := make(chan []byte, 8)
			b.sseMu.Lock()
			b.sseClients[ch] = struct{}{}
			b.sseMu.Unlock()

			defer func() {
				b.sseMu.Lock()
				delete(b.sseClients, ch)
				b.sseMu.Unlock()
				close(ch)
			}()

			// send current state immediately
			if data, err := json.Marshal(b.currentEvent()); err == nil {
				fmt.Fprintf(c.Writer, "data: %s\n\n", data)
				flusher.Flush()
			}

			notify := c.Request.Context().Done()
			for {
				select {
				case <-notify:
					return
				case data, ok := <-ch:
					if !ok {
						return
					}
					fmt.Fprintf(c.Writer, "data: %s\n\n", data)
					flusher.Flush()
				}
			}
		})

		// History (HISTORY panel).
		api.GET("/history", func(c *gin.Context) {
			c.JSON(http.StatusOK, b.history.List())
		})

		// RESET CLEAR button.
		api.DELETE("/history", func(c *gin.Context) {
			b.history.Clear()
			c.JSON(http.StatusOK, gin.H{"status": "cleared"})
		})
	}

	// Serve the frontend (single-binary desktop mode).
	r.Static("/assets", "./frontend/assets")
	r.StaticFile("/", "./frontend/index.html")
	r.NoRoute(func(c *gin.Context) {
		c.File("./frontend/index.html")
	})

	b.router = r
}
