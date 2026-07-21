{
  description = "Terminal-first calendar, todo, and journal manager";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
        # Single source of truth for the released version; the release
        # workflow refuses to run if VERSION does not match the tag.
        version = pkgs.lib.trim (builtins.readFile ./VERSION);
        # nixos-unstable still carries Go 1.26.4, which is affected by
        # GO-2026-5856. Keep Nix-built binaries on the fixed patch release
        # until the pinned nixpkgs input catches up.
        go = pkgs.go_1_26.overrideAttrs (_: {
          version = "1.26.5";
          src = pkgs.fetchurl {
            url = "https://go.dev/dl/go1.26.5.src.tar.gz";
            hash = "sha256-SVvkvIcXasVnOS5bQRar2YRm0z17SdQedkzMaXay3EI=";
          };
        });
        chroncal = pkgs.buildGoModule {
          pname = "chroncal";
          inherit version;

          src = ./.;
          subPackages = [ "cmd/chroncal" ];
          vendorHash = "sha256-mfZhpLWmoNf3A7WojP/TQXMtGrfm2OULl6UUS7oWT24=";

          nativeBuildInputs = [ go ];
          env.CGO_ENABLED = "0";

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
            "-X main.commit=${self.shortRev or "unknown"}"
            "-X main.date=${self.lastModifiedDate or "unknown"}"
          ];

          checkPhase = ''
            runHook preCheck
            HOME="$TMPDIR" GOFLAGS="" go test ./...
            runHook postCheck
          '';

          meta = {
            description = "Terminal-first calendar, todo, and journal manager";
            homepage = "https://github.com/DouglasdeMoura/chroncal";
            license = pkgs.lib.licenses.mit;
            mainProgram = "chroncal";
          };
        };
      in
      {
        packages = {
          default = chroncal;
          chroncal = chroncal;
        };

        apps.default = flake-utils.lib.mkApp {
          drv = self.packages.${system}.default;
          exePath = "/bin/chroncal";
        };

        devShells.default = pkgs.mkShell {
          packages = [
            go
            pkgs.golangci-lint
            pkgs.goreleaser
            pkgs.govulncheck
            pkgs.sqlc
          ];
        };
      }
    );
}
