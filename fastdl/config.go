package fastdl

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Version is reported by /api/health and shown in the app title bar.
const Version = "1.4.0"

// Config holds the application configuration.
type Config struct {
	Host string
	Port string

	// OutputDir is the folder where finished files land.
	OutputDir string

	// Aria2 RPC daemon settings.
	Aria2Port   int
	Aria2Secret string
	Aria2cPath  string // set during setup when found/downloaded
	FfmpegPath  string // set during setup when found
	YtdlpOK     bool   // true when go-ytdlp managed binary is installed
}

// DefaultConfig builds the config used by both the desktop app and the
// standalone server. Output goes to the real user Downloads folder so the
// installed app behaves the same no matter its working directory.
func DefaultConfig() *Config {
	out := filepath.Join(userHomeDir(), "Downloads", "YT Blaze")
	return &Config{
		Host:        "127.0.0.1",
		Port:        "8080",
		OutputDir:   out,
		Aria2Port:   6800,
		Aria2Secret: randomSecret(),
	}
}

// Addr returns host:port for the HTTP server.
func (c *Config) Addr() string { return c.Host + ":" + c.Port }

// EnsureOutputDir creates the downloads folder if missing.
func (c *Config) EnsureOutputDir() error { return os.MkdirAll(c.OutputDir, 0755) }

func userHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// randomSecret generates an aria2 RPC secret token.
func randomSecret() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "ytblaze"
	}
	return hex.EncodeToString(b)
}
