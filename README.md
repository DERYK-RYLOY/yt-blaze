# YT Blaze

Super-fast YouTube downloader for Windows — aria2 + yt-dlp backend with a dark gradient UI.

Built by DERYK RYLOY

## Features
- Real-time download progress with logo scan animation
- Native Windows chrome, dark theme
- Format/quality selection, history
- Hidden console windows — no black boxes
- NSIS installer with WebView2 runtime bootstrap

## Build
```bash
wails build -platform windows/amd64 -nsis
```
Artifacts: `build/bin/YT Blaze.exe` and `build/bin/ytblaze-desktop-amd64-installer.exe`

## Requirements
- Go 1.27+
- Wails v2
- NSIS for installer

## License
Copyright 2026 DERYK RYLOY
