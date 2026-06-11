# Arch Linux AUR Packaging

This directory contains maintained templates for Arch Linux packages:

| Directory | AUR package | What it does |
| --- | --- | --- |
| `chroncal/` | `chroncal` | Builds from the tagged source archive with the local Go toolchain |
| `chroncal-bin/` | `chroncal-bin` | Installs the prebuilt Linux release archive |

Each package lives in its own directory so it can be copied directly into the
matching AUR repository.

## Update For A New Release

1. Set `pkgver` to the release version without the leading `v`.
2. Reset `pkgrel` to `1`.
3. Update `sha256sums` from the release artifacts.
4. Run `makepkg --printsrcinfo > .SRCINFO` inside each package directory.
5. Run `makepkg -Csf` inside each package directory.
6. Commit the updated `PKGBUILD` and `.SRCINFO` files to the AUR package repos.

The `chroncal-bin` checksums come from the GitHub Release `checksums.txt` file.
The `chroncal` source checksum comes from GitHub's tagged source archive.

## Local Validation

```bash
cd packaging/aur/chroncal
makepkg --printsrcinfo >/tmp/chroncal.SRCINFO
makepkg -Csf

cd ../chroncal-bin
makepkg --printsrcinfo >/tmp/chroncal-bin.SRCINFO
makepkg -Csf
```

Use a clean Arch container or VM for final package validation before publishing.
