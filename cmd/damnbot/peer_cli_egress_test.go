//go:build !minimal

package main

import (
	"bytes"
	"crypto/ed25519"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gwpeer"
	"github.com/damnbot/damnbot/internal/netx"
)

// TestF7PeerDMEgress proves the peer DM send path crosses the registered
// gateway seam (GAP-OPS-02, GW-013): the fake transport records exactly one
// POST on the peer DM route and the request netx logs carries the peer-dm
// egress label. A second guard rejects a registry entry with the wrong lane.
func TestF7PeerDMEgress(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	tokenRef := "PEER_DM_" + "TOKEN"
	t.Setenv(tokenRef, gwpeer.NewID())
	var logBuf bytes.Buffer
	net := netx.New(netx.Config{Log: slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	t.Cleanup(net.CloseIdleConnections)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	client := &gwpeer.Client{LocalID: "a", Key: key, Tokens: gwpeer.EnvTokens{}, HTTP: peerDMClient(net)}
	peer := &gwpeer.Peer{ID: "b", URL: srv.URL, TokenRef: tokenRef, PublicKey: publicKeyOf(t, key)}
	if _, err := client.Send(t.Context(), peer, "dm", "hello"); err != nil {
		t.Fatalf("peer dm through the netx seam: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/peer/v1/dm" {
		t.Fatalf("fake transport paths = %v, want one /peer/v1/dm", paths)
	}
	if !strings.Contains(logBuf.String(), "egress=peer-dm") {
		t.Fatalf("egress label missing from the request log:\n%s", logBuf.String())
	}
	e, ok := netx.Registry()["peer-dm"]
	if !ok || e.Purpose != netx.Gateway {
		t.Fatalf(`registry["peer-dm"] = %+v (present %t), want a gateway entry`, e, ok)
	}
}
