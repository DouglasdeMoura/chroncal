# Installation

This page lists every supported way to install `chroncal`, plus the maintainer
work needed to keep those channels healthy.

## Recommended Options

| Method | Platforms | Best for |
| --- | --- | --- |
| Homebrew | macOS, Linux | Users who want managed install and upgrade commands |
| Go install | Any platform with Go 1.25+ | Go users and contributors |
| mise | macOS, Linux | Users who already manage tools with mise |
| GitHub Releases | Linux, macOS, Windows, FreeBSD, OpenBSD | Users who want prebuilt binaries without package managers |
| Build from source | Any platform with Go 1.25+ | Contributors and packagers |

Arch Linux packaging templates are maintained in `packaging/aur/` so the AUR
packages can be published and updated from this repository.

## Homebrew

```bash
brew tap douglasdemoura/tap && brew install chroncal
```

Upgrade:

```bash
brew update && brew upgrade chroncal
```

Uninstall:

```bash
brew uninstall chroncal
```

The release workflow updates `DouglasdeMoura/homebrew-tap` automatically when the
maintainer configures the `HOMEBREW_TAP_TOKEN` repository secret. If Homebrew is
temporarily unavailable for a new release, use GitHub Releases or `go install`.

## Go Install

Requires [Go](https://go.dev/) 1.25+.

```bash
go install github.com/douglasdemoura/chroncal/cmd/chroncal@latest
```

Pin an exact release:

```bash
go install github.com/douglasdemoura/chroncal/cmd/chroncal@v0.2.2
```

Make sure Go's binary directory is on your `PATH`:

```bash
go env GOPATH
```

The binary is usually installed to `$(go env GOPATH)/bin/chroncal`.

## mise

Install the latest GitHub release globally:

```bash
mise use -g github:DouglasdeMoura/chroncal
```

Pin an exact release globally:

```bash
mise use -g github:DouglasdeMoura/chroncal@0.2.2
```

If a just-published release does not appear yet, clear mise's GitHub release
cache first:

```bash
mise cache clear
mise ls-remote github:DouglasdeMoura/chroncal
```

## GitHub Releases

Download the archive for your platform from the
[latest release](https://github.com/DouglasdeMoura/chroncal/releases/latest).

Linux x86_64 example:

```bash
VERSION=0.2.2
PLATFORM=linux_amd64
curl -LO "https://github.com/DouglasdeMoura/chroncal/releases/download/v${VERSION}/chroncal_${VERSION}_${PLATFORM}.tar.gz"
curl -LO "https://github.com/DouglasdeMoura/chroncal/releases/download/v${VERSION}/checksums.txt"
sha256sum --ignore-missing -c checksums.txt
tar -xzf "chroncal_${VERSION}_${PLATFORM}.tar.gz"
sudo install chroncal /usr/local/bin/
```

macOS Apple Silicon example:

```bash
VERSION=0.2.2
PLATFORM=darwin_arm64
curl -LO "https://github.com/DouglasdeMoura/chroncal/releases/download/v${VERSION}/chroncal_${VERSION}_${PLATFORM}.tar.gz"
curl -LO "https://github.com/DouglasdeMoura/chroncal/releases/download/v${VERSION}/checksums.txt"
grep "chroncal_${VERSION}_${PLATFORM}.tar.gz" checksums.txt | shasum -a 256 -c -
tar -xzf "chroncal_${VERSION}_${PLATFORM}.tar.gz"
sudo install chroncal /usr/local/bin/
```

Windows users should download the `chroncal_<version>_windows_amd64.zip` asset,
extract it, and place `chroncal.exe` on `PATH`.

## Build From Source

```bash
git clone https://github.com/DouglasdeMoura/chroncal.git
cd chroncal
make build
./chroncal version
```

Run the test suite before sending changes:

```bash
go test ./...
```

## Arch Linux AUR

The repository includes package templates for two AUR packages:

```bash
yay -S chroncal-bin  # prebuilt Linux binary from GitHub Releases
yay -S chroncal      # builds from source with your local Go toolchain
```

`chroncal-bin` is fastest for x86_64 and aarch64 users. `chroncal` is the right
choice when you want to build locally or use another Arch-supported CPU target.

See `packaging/aur/README.md` for maintainer instructions.

## Maintainer Checklist

Before cutting a release:

1. Make sure CI is green on `master`.
2. Run `goreleaser check` locally if GoReleaser is installed.
3. Create a `v*` tag and push it.
4. Confirm the GitHub Release includes archives, `checksums.txt`, and install snippets.
5. Confirm `brew tap douglasdemoura/tap && brew install chroncal` works after the Homebrew tap update.
6. Confirm `go install github.com/douglasdemoura/chroncal/cmd/chroncal@<tag>` works.
7. Confirm `mise use -g github:DouglasdeMoura/chroncal@<tag>` resolves the release.

Required repository secrets:

| Secret | Purpose | Required |
| --- | --- | --- |
| `GITHUB_TOKEN` | Created automatically by GitHub Actions; publishes release assets | Yes |
| `HOMEBREW_TAP_TOKEN` | Personal access token with write access to `DouglasdeMoura/homebrew-tap` | No, but Homebrew updates are skipped without it |

## Future Package Channels

These are good follow-up contributions once the primary release flow is stable:

| Channel | Suggested artifact |
| --- | --- |
| Arch Linux AUR publishing | Copy `packaging/aur/chroncal` and `packaging/aur/chroncal-bin` into their AUR repos |
| Nix | `flake.nix` exposing `packages.default` and `apps.default` |
| Scoop | `packaging/scoop/chroncal.json` plus a maintained bucket |
| Debian / RPM | GoReleaser nFPM-generated `.deb` and `.rpm` assets |
