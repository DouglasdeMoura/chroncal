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
        go = pkgs.go_1_26;
        chroncal = pkgs.buildGoModule {
          pname = "chroncal";
          inherit version;

          src = ./.;
          subPackages = [ "cmd/chroncal" ];
          vendorHash = "sha256-b7jr+tG8ACbjaQ7txUaYXyy5e3ch65aNWDQzXpeLRz4=";

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
