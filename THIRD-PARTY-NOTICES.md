# Third-Party Notices

YT Blaze is MIT licensed. It builds on, downloads, and talks to the following
projects at runtime. Their licenses apply to them, not to this repository.

## Bundled at build time

| Component | License | Use |
| --- | --- | --- |
| [Wails v2](https://wails.io) | MIT | Desktop shell, WebView2 host, NSIS packaging |
| [Gin](https://github.com/gin-gonic/gin) | MIT | Local HTTP API |
| [go-ytdlp](https://github.com/lrstanley/go-ytdlp) | MIT | yt-dlp / ffmpeg provisioning and metadata extraction |
| [Go standard library](https://go.dev) | BSD-3-Clause | Everything else |
| [Press Start 2P](https://fonts.google.com/specimen/Press+Start+2P) | SIL OFL 1.1 | Display font |
| [Space Grotesk](https://fonts.google.com/specimen/Space+Grotesk) | SIL OFL 1.1 | UI font |

## Downloaded and executed on first run

YT Blaze installs these tools into the user's profile (`~/.ytblaze/bin` and the
go-ytdlp cache) on first launch. They are not redistributed in this repository
or in the installer.

| Component | License | Use |
| --- | --- | --- |
| [aria2](https://aria2.github.io) | GPL-2.0 | Segmented multi-connection transfer engine |
| [yt-dlp](https://github.com/yt-dlp/yt-dlp) | Unlicense | Metadata extraction and native fallback downloads |
| [FFmpeg](https://ffmpeg.org) | LGPL-2.1-or-later (build dependent) | Container remux / audio conversion |

If you redistribute a build that bundles any of the GPL/LGPL components,
you are responsible for complying with their licenses, including offering the
corresponding source.

## Trademarks

YouTube is a trademark of Google LLC. This project is not affiliated with,
endorsed by, or sponsored by Google LLC or YouTube.
