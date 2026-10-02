# Packaged license sources

The Qt license texts in `qt/` use the SPDX-named files distributed in Qt 6
source trees: LGPL 3.0, LGPL 2.1, GPL 3.0, GPL 2.0, and the Qt GPL exception.
They are kept in the repository because Qt's official prebuilt SDK archives do
not consistently include their `LICENSES` source directory. Official package
assembly also verifies the pinned Qt source archives in
`packaging/qt-sources-6.12.0.json`, copies their module attribution metadata and
referenced notices, and writes a deterministic `attributions/index.json`.
That index distinguishes payload matches from source or build provenance and
records whether the selected Qt kit supplied SPDX SBOM files.

`Apache-2.0.txt` is the Apache License 2.0 text used for resolved
SQLitePCLRaw packages. `DotNet-MIT.txt` is the .NET Foundation MIT license
distributed with the resolved .NET runtime packages. Package assembly also
copies package-specific nuspec files and any license or notice files shipped in
the exact NuGet and Go module versions used by the build.
