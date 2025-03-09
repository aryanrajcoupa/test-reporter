{
  description = "Nix-flake with go";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [
        "aarch64-darwin"
        "x86_64-darwin"
        "x86_64-linux"
      ];
    in
    {
      devShells = nixpkgs.lib.genAttrs supportedSystems (system:
        let
          pkgs = import nixpkgs {
            inherit system;
          };
          runPkgs = with pkgs; [
            go
            cmake
          ];
        in
        {
          default = pkgs.mkShell {
            packages = runPkgs;
            shellHook = ''
              export GO111MODULE=on
            '';
          };
        });
    };
}
