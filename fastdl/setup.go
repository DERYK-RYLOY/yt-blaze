package fastdl

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/lrstanley/go-ytdlp"
)

// aria2 official release for Windows amd64 (1.37.0).
const aria2WinURL = "https://github.com/aria2/aria2/releases/download/release-1.37.0/aria2-1.37.0-win-64bit-build1.zip"

// setupTools prepares yt-dlp, ffmpeg and aria2c:
//   - yt-dlp: go-ytdlp managed install (checksum-validated)
//   - ffmpeg: go-ytdlp managed install (used for merge/convert)
//   - aria2c: PATH lookup, else download official build into the app bin dir
func setupTools(cfg *Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. yt-dlp — auto-install into go-ytdlp cache. The managed binary is
	// versioned (e.g. yt-dlp-2026.10.21.exe), so glob for it.
	if hasYtdlp(cacheDirOrEmpty()) {
		cfg.YtdlpOK = true
	}
	if !cfg.YtdlpOK {
		ytdlp.MustInstall(ctx, nil)
		if hasYtdlp(cacheDirOrEmpty()) {
			cfg.YtdlpOK = true
		}
	}

	// 2. ffmpeg — try PATH first, then managed install.
	if p, err := lookPath("ffmpeg"); err == nil {
		cfg.FfmpegPath = p
	} else {
		if cacheDir, err := ytdlp.GetCacheDir(); err == nil {
			p := filepath.Join(cacheDir, "ffmpeg"+exeSuffix())
			if _, statErr := os.Stat(p); statErr == nil {
				cfg.FfmpegPath = p
			}
		}
		if cfg.FfmpegPath == "" {
			// managed download (windows/linux supported by go-ytdlp)
			func() {
				defer func() { _ = recover() }() // Must* panics on unsupported platform
				ytdlp.MustInstallFFmpeg(ctx, nil)
			}()
			if cacheDir, err := ytdlp.GetCacheDir(); err == nil {
				p := filepath.Join(cacheDir, "ffmpeg"+exeSuffix())
				if _, statErr := os.Stat(p); statErr == nil {
					cfg.FfmpegPath = p
				}
			}
		}
	}

	// 3. aria2c — PATH first, else download the official build (Windows).
	if p, err := lookPath("aria2c"); err == nil {
		cfg.Aria2cPath = p
	} else if runtime.GOOS == "windows" {
		if p, err := ensureAria2Windows(ctx); err == nil {
			cfg.Aria2cPath = p
		} else {
			return fmt.Errorf("aria2 unavailable (native fallback will be used): %w", err)
		}
	} else {
		return fmt.Errorf("aria2 unavailable (native fallback will be used)")
	}
	return nil
}

// ensureAria2Windows downloads and extracts aria2c.exe into ~/.ytblaze/bin.
func ensureAria2Windows(ctx context.Context) (string, error) {
	binDir, err := appBinDir()
	if err != nil {
		return "", err
	}
	exe := filepath.Join(binDir, "aria2c.exe")
	if _, err := os.Stat(exe); err == nil {
		return exe, nil
	}

	zipPath := filepath.Join(binDir, "aria2.zip")
	if err := downloadFile(ctx, aria2WinURL, zipPath); err != nil {
		return "", fmt.Errorf("aria2 download failed: %w", err)
	}
	defer os.Remove(zipPath)

	if err := extractZipEntry(zipPath, "aria2-1.37.0-win-64bit-build1/aria2c.exe", exe); err != nil {
		return "", fmt.Errorf("aria2 extract failed: %w", err)
	}
	return exe, nil
}

func appBinDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ytblaze", "bin")
	return dir, os.MkdirAll(dir, 0755)
}

func downloadFile(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// extractZipEntry pulls one file out of a zip archive.
func extractZipEntry(zipPath, entry, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.EqualFold(f.Name, entry) {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			defer out.Close()
			_, err = io.Copy(out, rc)
			return err
		}
	}
	return fmt.Errorf("entry %s not found in zip", entry)
}

func lookPath(name string) (string, error) {
	// os/exec LookPath with .exe handled automatically on Windows.
	return exec.LookPath(name)
}

// cacheDirOrEmpty returns the go-ytdlp cache dir or "" on error.
func cacheDirOrEmpty() string {
	d, err := ytdlp.GetCacheDir()
	if err != nil {
		return ""
	}
	return d
}

// hasYtdlp reports whether a yt-dlp binary (any version) exists in dir.
func hasYtdlp(dir string) bool {
	if dir == "" {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "yt-dlp*"+exeSuffix()))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
