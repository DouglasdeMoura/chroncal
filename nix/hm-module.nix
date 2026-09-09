# home-manager module for chroncal.
#
# The flake applies this file to itself. The module then reads the package
# that the flake builds for the system of the host configuration.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.chroncal;

  tomlFormat = pkgs.formats.toml { };

  # The flake builds a package for each default system. On another system the
  # module falls back to the nixpkgs package, and then to null. A null package
  # installs nothing, so the user can supply the program with another method.
  flakePackage = self.packages.${pkgs.stdenv.hostPlatform.system}.chroncal or null;
  nixpkgsPackage = pkgs.chroncal or null;
  defaultPackage = if flakePackage != null then flakePackage else nixpkgsPackage;

  # chroncal reads $XDG_CONFIG_HOME/chroncal/config.toml. macOS has no XDG
  # variable by default, so the program reads the user configuration
  # directory. home-manager exports XDG_CONFIG_HOME when xdg.enable is true.
  configHome =
    if pkgs.stdenv.hostPlatform.isDarwin && !config.xdg.enable then
      "${config.home.homeDirectory}/Library/Application Support"
    else
      config.xdg.configHome;
in
{
  options.programs.chroncal = {
    enable = lib.mkEnableOption "chroncal, a terminal-first calendar, todo, and journal manager";

    package = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      default = defaultPackage;
      defaultText = lib.literalExpression "chroncal.packages.\${pkgs.stdenv.hostPlatform.system}.chroncal";
      description = ''
        The chroncal package to install. Set the option to `null` to install
        the program with another method.
      '';
    };

    settings = lib.mkOption {
      inherit (tomlFormat) type;
      default = { };
      example = lib.literalExpression ''
        {
          product_id = "-//example//chroncal//EN";
          ui = {
            theme = "default";
            week_start = "monday";
          };
          soft_delete.purge_days = 30;
          sync = {
            interval = "15m";
            conflict_strategy = "prompt";
          };
        }
      '';
      description = ''
        The configuration for chroncal. home-manager writes the attribute set
        to {file}`$XDG_CONFIG_HOME/chroncal/config.toml` in the TOML format.

        The program reads these keys:

        - `db`: the path to the SQLite database file.
        - `product_id`: the iCal PRODID for an export.
        - `[smtp]`: `host`, `port`, `username`, `password`, `from`, and `tls`
          for an EMAIL alarm.
        - `[sync]`: `interval` and `conflict_strategy` for a CalDAV sync.
        - `[security]`: `allow_unsafe_alarm_audio_attach`,
          `allow_unsafe_alarm_email_attendees`, and `allow_plaintext`.
        - `[soft_delete]`: `purge_days` for the retention of a deleted row.
        - `[ui]`: `theme` and `week_start` for the TUI.

        Do not put a secret in this attribute set. The Nix store is readable
        for every user of the machine. Use an environment variable, for
        example `CHRONCAL_SMTP_PASSWORD`, for a secret.

        See <https://github.com/DouglasdeMoura/chroncal#configuration> for the
        description and the default of each key.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = lib.mkIf (cfg.package != null) [ cfg.package ];

    home.file."${configHome}/chroncal/config.toml" = lib.mkIf (cfg.settings != { }) {
      source = tomlFormat.generate "chroncal-config.toml" cfg.settings;
    };
  };
}
