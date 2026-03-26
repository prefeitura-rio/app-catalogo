{
  description = "app-catalogo - Busca global e recomendação inteligente da Prefeitura Rio";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Go development
            go
            gotools
            gopls
            golangci-lint
            air
            go-swag

            # Database tools
            postgresql_16

            # Redis
            redis

            # Development tools
            just
            direnv

            # Docker
            docker
            docker-compose

            # General utilities
            jq
            curl
            git
          ];

          shellHook = ''
            echo "app-catalogo - Busca global e recomendacao"
            echo "==========================================="
            echo ""
            echo "Quick start:"
            echo "  1. cp .env.example .env"
            echo "  2. just up        # Start infrastructure"
            echo "  3. just migrate   # Run migrations"
            echo "  4. just dev       # Start development server"
            echo ""
            echo "Run 'just' to see all available commands"
          '';
        };
      }
    );
}
