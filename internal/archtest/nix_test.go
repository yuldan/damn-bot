package archtest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestNixFlakeParse (GAP-INF-03/OPS-030, ADR-033 Q49) validates the committed
// flake without requiring Nix: flake.lock is a pinned nixpkgs input and
// flake.nix exposes packages.default (static CGO=0 build via `go build -tags
// goolm`) and a devShell for the four supported systems. When
// DAMNBOT_NIX_CHECK=1 (CI job `nix` on a runner that has Nix), the same test
// runs `nix flake check` as proof; a missing Nix then fails instead of skipping.
func TestNixFlakeParse(t *testing.T) {
	t.Parallel()
	flake := readRepoFile(t, "flake.nix")
	for _, want := range []string{
		"packages", "default = pkgs.buildGoModule",
		"go build", "CGO_ENABLED=0", "-tags goolm",
		"vendorHash", "devShells", "mkShell",
		"x86_64-linux", "aarch64-linux", "x86_64-darwin", "aarch64-darwin",
	} {
		if !strings.Contains(flake, want) {
			t.Errorf("flake.nix tidak memuat %q", want)
		}
	}
	checkFlakeLock(t, readRepoFile(t, "flake.lock"))
	if os.Getenv("DAMNBOT_NIX_CHECK") != "1" {
		return
	}
	runNixFlakeCheck(t)
}

func checkFlakeLock(t *testing.T, raw string) {
	t.Helper()
	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				LastModified int64  `json:"lastModified"`
				NarHash      string `json:"narHash"`
				Rev          string `json:"rev"`
				Type         string `json:"type"`
			} `json:"locked"`
			Original struct {
				Owner string `json:"owner"`
				Ref   string `json:"ref"`
				Repo  string `json:"repo"`
				Type  string `json:"type"`
			} `json:"original"`
			Inputs map[string]any `json:"inputs"`
		} `json:"nodes"`
		Root    string `json:"root"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &lock); err != nil {
		t.Fatalf("flake.lock bukan JSON valid: %v", err)
	}
	if lock.Version != 7 || lock.Root != "root" {
		t.Errorf("flake.lock version/root = %d/%q, want 7/root", lock.Version, lock.Root)
	}
	np, ok := lock.Nodes["nixpkgs"]
	if !ok {
		t.Fatal("flake.lock: input nixpkgs tidak ada")
	}
	if np.Original.Type != "github" || np.Original.Owner != "NixOS" || np.Original.Repo != "nixpkgs" {
		t.Errorf("flake.lock nixpkgs original = %+v, want github:NixOS/nixpkgs", np.Original)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(np.Locked.Rev) {
		t.Errorf("flake.lock nixpkgs rev = %q, want commit 40-hex", np.Locked.Rev)
	}
	if !strings.HasPrefix(np.Locked.NarHash, "sha256-") || np.Locked.LastModified <= 0 {
		t.Errorf("flake.lock nixpkgs narHash/lastModified = %q/%d", np.Locked.NarHash, np.Locked.LastModified)
	}
	root, ok := lock.Nodes["root"]
	if !ok || root.Inputs["nixpkgs"] != "nixpkgs" {
		t.Errorf("flake.lock root.inputs.nixpkgs = %v, want nixpkgs", root.Inputs)
	}
}

func runNixFlakeCheck(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nix"); err != nil {
		t.Fatalf("DAMNBOT_NIX_CHECK=1: nix tidak tersedia: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nix", "flake", "check")
	cmd.Dir = repoDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nix flake check: %v\n%s", err, out)
	}
}
