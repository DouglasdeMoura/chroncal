# Home Manager module for chroncal.
#
# The shape follows the khal module in home-manager, scoped to what chroncal
# reads from $XDG_CONFIG_HOME/chroncal/config.toml. chroncal stores accounts
# in its database and the OS keyring, so this module exposes no account
# options. Use the freeform settings option for every documented config key,
# including smtp.password_cmd for command-retrieved passwords.
{ self }:

{
  lib,
  pkgs,
  config,
  ...
}:

let
  inherit (lib)
    mkEnableOption
    mkIf
    mkOption
    optional
    types
    ;

  cfg = config.programs.chroncal;
  settingsFormat = pkgs.formats.toml { };
in
{
  options.programs.chroncal = {
    enable = mkEnableOption "chroncal";

    package = mkOption {
      type = types.nullOr types.package;
      # `or null` covers systems outside the flake's default systems.
      default = self.packages.${pkgs.system}.default or null;
      defaultText = lib.literalExpression "chroncal.packages.\${pkgs.system}.default";
      description = ''
        The chroncal package to install.
        Set it to null to use a chroncal from another source.
      '';
    };

    settings = mkOption {
      type = types.submodule {
        freeformType = settingsFormat.type;
      };
      default = { };
      description = ''
        Settings for chroncal's config.toml.
        The module writes them to {file}`$XDG_CONFIG_HOME/chroncal/config.toml`.
        See the chroncal README for the supported keys.
        Do not put a literal smtp.password here. The Nix store is
        world-readable; use password_cmd for the secret instead.
      '';
      example = {
        ui = {
          theme = "default";
          week_start = "monday";
        };
        smtp = {
          host = "smtp.example.com";
          username = "me@example.com";
          from = "me@example.com";
          password_cmd = "pass show smtp/app-password";
        };
      };
    };
  };

  config = mkIf cfg.enable {
    home.packages = optional (cfg.package != null) cfg.package;

    xdg.configFile."chroncal/config.toml" = mkIf (cfg.settings != { }) {
      source = settingsFormat.generate "chroncal-config.toml" cfg.settings;
    };
  };
}
