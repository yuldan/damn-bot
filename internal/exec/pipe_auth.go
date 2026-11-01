package exec

// Peer authorization for the Windows control pipe (EC-WIN-008), kept free of
// Windows types so the decision itself is tested on every OS: the token side
// (peerSIDFromToken) extracts a SID string, and this file decides whether that
// identity is the one the server accepts.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrPeerRejected reports a connection whose token identity is not the one the
// server accepts. The connection is already closed when it is returned.
var ErrPeerRejected = errors.New("exec: pipe peer rejected")

const (
	sidPrefix        = "S-1-"
	sidMaxAuthority  = 1<<48 - 1
	sidMaxSub        = 1<<32 - 1
	sidMaxSubAuth    = 15
	sidMaxPartDigits = 20
)

// authorizePeer accepts exactly the expected Windows user SID. Empty and
// malformed SIDs are rejected on either side, and the comparison uses the
// canonical S-1-... form so "S-1-05-18" and "S-1-5-18" are the same account.
func authorizePeer(peerSID, expectedSID string) error {
	peer, ok := canonicalSID(peerSID)
	if !ok {
		return fmt.Errorf("%w: peer SID %q is empty or malformed", ErrPeerRejected, peerSID)
	}
	want, ok := canonicalSID(expectedSID)
	if !ok {
		return fmt.Errorf("%w: expected SID %q is empty or malformed", ErrPeerRejected, expectedSID)
	}
	if peer != want {
		return fmt.Errorf("%w: peer SID %s", ErrPeerRejected, peer)
	}
	return nil
}

// canonicalSID parses a SID string and re-renders it without leading zeros.
// Bounds follow the Windows layout: 48-bit authority, up to 15 32-bit
// sub-authorities.
func canonicalSID(s string) (string, bool) {
	parts := strings.Split(s, "-")
	if len(parts) < 3 || len(parts) > 3+sidMaxSubAuth || parts[0] != "S" || parts[1] != "1" {
		return "", false
	}
	authority, ok := sidPart(parts[2], sidMaxAuthority)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString(sidPrefix)
	b.WriteString(strconv.FormatUint(authority, 10))
	for _, p := range parts[3:] {
		sub, ok := sidPart(p, sidMaxSub)
		if !ok {
			return "", false
		}
		b.WriteByte('-')
		b.WriteString(strconv.FormatUint(sub, 10))
	}
	return b.String(), true
}

// sidPart parses one decimal component and rejects values that do not fit its
// Windows width.
func sidPart(s string, maxValue uint64) (uint64, bool) {
	if s == "" || len(s) > sidMaxPartDigits {
		return 0, false
	}
	var v uint64
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + uint64(c-'0')
		if v > maxValue {
			return 0, false
		}
	}
	return v, true
}
