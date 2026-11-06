package buzz

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrKey rejects a private key that is not a valid secp256k1 scalar.
var ErrKey = errors.New("buzz: invalid private key")

// Point arithmetic is only used for BIP340 signing of Nostr events. Affine
// coordinates with one modular inverse per operation are fast enough for the
// signing rate of a chat adapter and keep the arithmetic auditable.

func curveP() *big.Int {
	return hexInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F")
}

func curveN() *big.Int {
	return hexInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
}

func baseX() *big.Int {
	return hexInt("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798")
}

func baseY() *big.Int {
	return hexInt("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8")
}

func hexInt(s string) *big.Int {
	v, _ := new(big.Int).SetString(s, 16)
	return v
}

type point struct {
	x, y *big.Int
	inf  bool
}

func generator() point { return point{x: baseX(), y: baseY()} }

func modP(v *big.Int) *big.Int { return v.Mod(v, curveP()) }

func pointDouble(p point) point {
	if p.inf || p.y.Sign() == 0 {
		return point{inf: true}
	}
	num := new(big.Int).Mul(p.x, p.x)
	num.Mul(num, big.NewInt(3))
	den := new(big.Int).Lsh(p.y, 1)
	den.ModInverse(modP(den), curveP())
	lambda := modP(new(big.Int).Mul(num, den))
	x := new(big.Int).Mul(lambda, lambda)
	x.Sub(x, p.x)
	x.Sub(x, p.x)
	x = modP(x)
	y := new(big.Int).Sub(p.x, x)
	y.Mul(lambda, y)
	y.Sub(y, p.y)
	return point{x: x, y: modP(y)}
}

func pointAdd(a, b point) point {
	if a.inf {
		return b
	}
	if b.inf {
		return a
	}
	if a.x.Cmp(b.x) == 0 {
		if a.y.Cmp(b.y) != 0 || a.y.Sign() == 0 {
			return point{inf: true}
		}
		return pointDouble(a)
	}
	num := new(big.Int).Sub(b.y, a.y)
	den := new(big.Int).Sub(b.x, a.x)
	den.ModInverse(modP(den), curveP())
	lambda := modP(new(big.Int).Mul(num, den))
	x := new(big.Int).Mul(lambda, lambda)
	x.Sub(x, a.x)
	x.Sub(x, b.x)
	x = modP(x)
	y := new(big.Int).Sub(a.x, x)
	y.Mul(lambda, y)
	y.Sub(y, a.y)
	return point{x: x, y: modP(y)}
}

func scalarBaseMult(k *big.Int) point {
	result := point{inf: true}
	addend := generator()
	for i := range k.BitLen() {
		if k.Bit(i) == 1 {
			result = pointAdd(result, addend)
		}
		addend = pointDouble(addend)
	}
	return result
}

func parseKey(raw string) ([32]byte, error) {
	var out [32]byte
	decoded, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(raw), "0x"))
	if err != nil || len(decoded) != 32 {
		return out, ErrKey
	}
	d := new(big.Int).SetBytes(decoded)
	if d.Sign() == 0 || d.Cmp(curveN()) >= 0 {
		return out, ErrKey
	}
	copy(out[:], decoded)
	return out, nil
}

// publicKey returns the BIP340 x-only public key of priv.
func publicKey(priv [32]byte) [32]byte {
	p := scalarBaseMult(new(big.Int).SetBytes(priv[:]))
	var out [32]byte
	p.x.FillBytes(out[:])
	return out
}

func taggedHash(tag string, parts ...[]byte) [32]byte {
	tagHash := sha256.Sum256([]byte(tag))
	h := sha256.New()
	_, _ = h.Write(tagHash[:])
	_, _ = h.Write(tagHash[:])
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// signSchnorr produces a BIP340 signature. aux is optional fresh entropy; the
// zero value is valid and keeps tests deterministic.
func signSchnorr(priv [32]byte, msg [32]byte, aux []byte) ([64]byte, error) {
	var sig [64]byte
	n := curveN()
	d0 := new(big.Int).SetBytes(priv[:])
	if d0.Sign() == 0 || d0.Cmp(n) >= 0 {
		return sig, ErrKey
	}
	bigP := scalarBaseMult(d0)
	px := make([]byte, 32)
	bigP.x.FillBytes(px)
	d := d0
	if bigP.y.Bit(0) == 1 {
		d = new(big.Int).Sub(n, d0)
	}
	dBytes := make([]byte, 32)
	d.FillBytes(dBytes)
	auxHash := taggedHash("BIP0340/aux", aux)
	auxXor := make([]byte, 32)
	for i := range auxXor {
		auxXor[i] = dBytes[i] ^ auxHash[i]
	}
	k0 := new(big.Int).SetBytes(taggedHashBytes("BIP0340/nonce", auxXor, px, msg[:]))
	k0.Mod(k0, n)
	if k0.Sign() == 0 {
		return sig, fmt.Errorf("%w: nonce is zero", ErrKey)
	}
	bigR := scalarBaseMult(k0)
	rx := make([]byte, 32)
	bigR.x.FillBytes(rx)
	k := k0
	if bigR.y.Bit(0) == 1 {
		k = new(big.Int).Sub(n, k0)
	}
	e := new(big.Int).SetBytes(taggedHashBytes("BIP0340/challenge", rx, px, msg[:]))
	e.Mod(e, n)
	s := new(big.Int).Mul(e, d)
	s.Add(s, k)
	s.Mod(s, n)
	copy(sig[:32], rx)
	s.FillBytes(sig[32:])
	return sig, nil
}

func taggedHashBytes(tag string, parts ...[]byte) []byte {
	sum := taggedHash(tag, parts...)
	return sum[:]
}
