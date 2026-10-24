package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"fastdl"
)

//go:embed all:frontend
var frontendFS embed.FS

// App holds runtime state for the desktop app.
type App struct {
	ctx context.Context
	cfg *fastdl.Config
	ln  net.Listener

	mu      sync.Mutex
	backend *fastdl.Backend
	apiBase string
	ready   bool
	initErr string
}

func main() {
	cfg := fastdl.DefaultConfig()
	app := &App{cfg: cfg}

	err := wails.Run(&options.App{
		Title:     "YT Blaze",
		Width:     1280,
		Height:    800,
		MinWidth:  1024,
		MinHeight: 700,
		// Native Windows chrome: real title bar with min / max / close and the
		// flame icon. The UI below is full-bleed content, no fake frame.
		AssetServer:      &assetserver.Options{Assets: frontendFS},
		BackgroundColour: &options.RGBA{R: 20, G: 22, B: 29, A: 1},
		// Dark Windows 10/11 chrome so the native title bar matches the UI.
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.Dark,
			CustomTheme: &windows.ThemeSettings{
				DarkModeTitleBar:  windows.RGB(20, 22, 29),
				DarkModeTitleText: windows.RGB(232, 234, 242),
				DarkModeBorder:    windows.RGB(20, 22, 29),
			},
		},
		OnStartup:  app.startup,
		OnShutdown: app.shutdown,
		Bind:       []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}

// startup reserves a race-free port for the API before the window paints and
// boots the backend in the background, so the UI opens instantly even while
// yt-dlp / ffmpeg / aria2 are still being downloaded on first run.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	if err := a.cfg.EnsureOutputDir(); err != nil {
		log.Printf("output dir warning: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		a.mu.Lock()
		a.initErr = fmt.Sprintf("no free local port: %v", err)
		a.mu.Unlock()
	} else {
		a.mu.Lock()
		a.ln = ln
		a.apiBase = fmt.Sprintf("http://127.0.0.1:%d/api", ln.Addr().(*net.TCPAddr).Port)
		base := a.apiBase
		a.mu.Unlock()
		log.Printf("api will listen on %s", base)
	}

	// aria2 RPC daemon port: also dynamic so two app copies never collide.
	if p, err := freePort(); err == nil {
		a.cfg.Aria2Port = p
	}

	go a.boot()
}

// boot builds the backend then serves it on the reserved listener.
func (a *App) boot() {
	a.mu.Lock()
	ln, hadListener := a.ln, a.ln != nil
	a.mu.Unlock()
	if !hadListener {
		return // initErr already set in startup
	}
	var b *fastdl.Backend
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		b, err = fastdl.NewBackend(a.cfg)
		if err == nil {
			break
		}
		if attempt == 3 {
			break
		}
		select {
		case <-a.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}

	a.mu.Lock()
	if err != nil {
		a.initErr = err.Error()
		a.mu.Unlock()
		log.Printf("backend init failed: %v", err)
		return
	}
	a.backend = b
	a.ready = true
	a.mu.Unlock()
	log.Print("backend ready")

	go func() {
		if err := b.Serve(ln); err != nil && !errors.Is(err, net.ErrClosed) {
			log.Printf("api server stopped: %v", err)
		}
	}()
}

// ---- bindings the frontend calls via window.go.main.App.* ----

// APIBase returns the REST/SSE base URL, e.g. "http://127.0.0.1:54321/api".
func (a *App) APIBase() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.apiBase
}

// OutputDir returns the folder finished downloads land in.
func (a *App) OutputDir() string { return a.cfg.OutputDir }

// InitError returns a non-empty message when backend boot failed outright.
func (a *App) InitError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.initErr
}

// ShowOutputDir reveals the downloads folder in Explorer (folder selected).
func (a *App) ShowOutputDir() error {
	if err := a.cfg.EnsureOutputDir(); err != nil {
		return err
	}
	// quotes so paths with spaces (e.g. Downloads\YT Blender) survive explorer's parser
	return exec.Command("explorer", "/select,\""+a.cfg.OutputDir+"\"").Start()
}

// shutdown stops the backend and its aria2 daemon.
func (a *App) shutdown(ctx context.Context) {
	if a.backend != nil {
		a.backend.Shutdown()
	}
	if a.ln != nil {
		_ = a.ln.Close()
	}
}

// freePort grabs an ephemeral port (best effort).
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p, nil
}
