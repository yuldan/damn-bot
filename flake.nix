{
  description = "Damn-Bot — satu binary Go statis, tanpa runtime Python/Node (ADR-001, ADR-011, GAP-INF-03)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";

  outputs = { self, nixpkgs }:
    let
      # Empat target pengembangan utama; rilis kanonik tetap 8 target E-39 yang dibangun
      # `make build-matrix` (Nix hanya jalur paket/devShell, bukan pengganti gate matrix).
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      each = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      packages = each (pkgs: {
        default = pkgs.buildGoModule {
          pname = "damnbot";
          version = "0.0.0";
          src = self;
          # Hash direktori `go mod vendor` pada go.mod/go.sum yang di-commit.
          vendorHash = "sha256-B72ROt/Qv1R9mkJTSSno4WwKqs5ugFk/FCq+9MeNJP4=";
          doCheck = false;
          env.CGO_ENABLED = "0";
          ldflags = [ "-s" "-w" ];
          buildPhase = ''
            runHook preBuild
            export CGO_ENABLED=0
            go build -trimpath -tags goolm -ldflags "-s -w" -o damnbot ./cmd/damnbot
            runHook postBuild
          '';
          installPhase = ''
            runHook preInstall
            install -Dm755 damnbot $out/bin/damnbot
            runHook postInstall
          '';
          meta = {
            description = "Damn-Bot agent binary (static, CGO disabled)";
            homepage = "https://github.com/damnbot/damnbot";
            license = pkgs.lib.licenses.mit;
            mainProgram = "damnbot";
            platforms = pkgs.lib.platforms.linux ++ pkgs.lib.platforms.darwin;
          };
        };
      });

      devShells = each (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.git pkgs.gnumake ];
          CGO_ENABLED = "0";
          shellHook = ''
            echo "damnbot devShell — $(go version | ${pkgs.coreutils}/bin/cut -d' ' -f3), CGO_ENABLED=0, tag goolm"
          '';
        };
      });

      formatter = each (pkgs: pkgs.nixfmt-rfc-style);
    };
}
