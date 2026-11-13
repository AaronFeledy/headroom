# Provider marks

Bundled on 2026-09-07 from the providers' own sites. These assets identify the
services; their respective owners retain rights to their trademarks. They are
not covered by this repository's MIT license. No endorsement is implied.

| File | Original source |
| --- | --- |
| `claude.svg` | https://claude.ai/favicon.svg |
| `codex.svg` | https://chatgpt.com/unauth-mweb/images/favicon.svg (ChatGPT favicon, refreshed 2026-09-08) |
| `cursor.svg` | https://cursor.com/marketing-static/favicon.svg (dark appearance) |
| `grok.svg` | https://grok.com/images/favicon.svg |
| `opencodego.svg` | https://opencode.ai/favicon.svg (OpenCode favicon, added 2026-11-13) |

ChatGPT uses the favicon’s official dark-mode white fill explicitly because Qt
SVG does not evaluate its browser color-scheme media query. Its path geometry is
unchanged; the filename remains `codex` for compatibility with the API provider
key. OpenCode Go drops the favicon’s wrapper `<svg>` and its no-op color-scheme
`<style>` block; its shapes and fills are unchanged, and the filename matches the
API provider key `OpenCodeGo`. Other SVGs are the original downloaded assets. PNGs are 128 × 128 rasterizations for
WinForms, generated with `rsvg-convert -w 128 -h 128 -o NAME.png NAME.svg`
(`opencodego.png` with ImageMagick `convert -sample 128x128`; its shapes are
pixel-aligned at that size).
Neither client downloads icons at runtime. Linux embeds SVGs; Windows embeds PNGs.
