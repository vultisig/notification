{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    systems.url = "github:nix-systems/default";
    devenv.url = "github:cachix/devenv";
  };

  outputs = { self, nixpkgs, devenv, systems, ... } @ inputs:
    let
      forEachSystem = nixpkgs.lib.genAttrs (import systems);
    in
    {
      devShells = forEachSystem
        (system:
          let
            pkgs = nixpkgs.legacyPackages.${system};
          in
          {
            default = devenv.lib.mkShell {
              inherit inputs pkgs;
              modules = [
                {
                  languages.go = {
                    enable = true;
                  };

                  services.mysql = {
                    enable = true;
                    package = pkgs.mysql84;
                    initialDatabases = [
                      {
                        name = "notification";
                      }
                    ];
                  };

                  services.redis = {
                    enable = true;
                    package = pkgs.redis;
                  };

                  packages = with pkgs; [
                    go-ethereum
                    sqlc
                    goose
                    tailwindcss
                  ];

                  enterShell = ''
                    echo "notification shell started!"
                  '';
                }
              ];
            };
          });
    };
}
