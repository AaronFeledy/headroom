# README renderings

The README overview is generated from the current desktop QML at a compact
539-pixel width, with synthetic readings and a platform-neutral tray. The panel
height is fitted to all four providers, so no meters are hidden by scrolling.
The composition enlarges the panel to 150% and draws the tray at 62.5% of its
standalone size. This brings the provider icons and text into proportion with
the tray. The app keeps its original compact layout.

## Regenerate

Requirements: Python 3.10+ with Pillow, CMake 3.25+, a C++17 toolchain, and the
same Qt 6.12+ development modules required by the desktop build. Linux/WSL is the
verified rendering environment. No desktop session or running Headroom server
is needed.

From the repository root:

```bash
python3 docs/renderings/render.py --qt-prefix /path/to/Qt/6.12.0/gcc_64
```

You can omit `--qt-prefix` if `CMAKE_PREFIX_PATH` already points to Qt.
The build lives in the ignored `.omo/docs-render-build` folder. It builds only
the documentation renderer and shared desktop library; it does not build or run
the test suite. It never invokes a reset action.

For a data or composition edit after the renderer has been built:

```bash
python3 docs/renderings/render.py --skip-build
```

Run the full command again after C++ changes. QML is loaded directly from the
source tree on every render.

## Editable sources

- `scene.json`: compact width, export scale, frozen clock, provider order and
  illustrative readings. `reset_in_seconds` is measured from the frozen clock.
  Keep `primary` aligned with the first provider. Subtitles are illustrative
  plan labels, not a list of available plans. `composition.ui_scale` controls
  panel zoom independently of `composition.tray_scale`; `composition.width`
  sets the surrounding canvas width. Increase the canvas if either piece no
  longer fits.
- `clients/desktop/qml/`: the actual UI. There is no duplicated mock interface.
  Layout, labels, meter colors, warnings and pace calculations follow the app.
- `tray.svg.in`: neutral tray shell and status icons. Text is converted to
  vector outlines, and the provider artwork is embedded from the shared assets.
- `render.cpp`: isolated, offscreen QML renderer and frozen-clock adapter.
  Credentials, polling, automatic migration and public update requests are
  disabled; settings are created in a temporary directory.
- `render.py`: builds the renderer, generates the tray, composes the panel and
  tray, validates exports and records source hashes. Its small vector geometry
  adapter mirrors the steady usage state in `trayvisual.cpp`; review the adapter
  when the tray drawing geometry changes. Usage, pace and severity come from the
  production `TrayVisual::build` model. Animated attention is shown in its
  resting frame.
- `fonts/`: unmodified Noto Sans Regular and Bold, with the supplied copyright
  and SIL Open Font License 1.1 notice. Bundling the fonts keeps documentation
  typography consistent across machines. This is the Linux fallback for the
  app's preferred Inter face; it does not change the app's font configuration.

## Generated assets

All outputs are written to `docs/images/` after successful generation:

| Asset | Purpose |
| --- | --- |
| `headroom-overview.png` | Combined panel and tray used by the README; 2× resolution by default |
| `headroom-overview.svg` | Self-contained composition with an embedded high-resolution UI and vector tray |
| `headroom-desktop.png` | Standalone current compact UI render |
| `headroom-tray.svg` | Fully vector standalone tray with a Headroom tooltip |
| `headroom-tray.png` | High-resolution standalone tray |
| `headroom-render.json` | Qt version, font, dimensions, geometry, tray model and source hashes |

The surrounding canvas is transparent for light and dark README themes. The
combined SVG embeds the UI as a PNG; it is not a fully vector UI. To increase UI
resolution, change `scale` in the scene and regenerate.
The panel is rendered at scale multiplied by composition.ui_scale (3x with the
default settings), so composition zoom never enlarges a lower-resolution bitmap.
The final overview remains a 2x export.

The renderer checks that every provider card and meter fits, that all images
have the expected size and alpha channel, and that the SVG parses. Inspect the
combined PNG after regeneration. Identical sources, fonts, Qt and rendering
environment should produce identical image files. Qt or platform rasterizer
updates can change antialiasing; the metadata identifies the inputs.

Use `--scene PATH` for another synthetic scene and `--output-dir PATH` to preview
without replacing the README assets. Do not supply captured live account data.