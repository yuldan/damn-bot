package buzz

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// BIP340 reference vectors (bip-0340/test-vectors.csv, rows 0 and 1).
func TestSchnorrBIP340Vectors(t *testing.T) {
	vectors := []struct {
		sk, pk, aux, msg, sig string
	}{
		{
			sk:  "0000000000000000000000000000000000000000000000000000000000000003",
			pk:  "F9308A019258C31049344F85F89D5229B531C845836F99B08601F113BCE036F9",
			aux: "0000000000000000000000000000000000000000000000000000000000000000",
			msg: "0000000000000000000000000000000000000000000000000000000000000000",
			sig: "E907831F80848D1069A5371B402410364BDF1C5F8307B0084C55F1CE2DCA821525F66A4A85EA8B71E482A74F382D2CE5EBEEE8FDB2172F477DF4900D310536C0",
		},
		{
			sk:  "B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF",
			pk:  "DFF1D77F2A671C5F36183726DB2341BE58FEAE1DA2DECED843240F7B502BA659",
			aux: "0000000000000000000000000000000000000000000000000000000000000001",
			msg: "243F6A8885A308D313198A2E03707344A4093822299F31D0082EFA98EC4E6C89",
			sig: "6896BD60EEAE296DB48A229FF71DFE071BDE413E6D43F917DC8DCF8C78DE33418906D11AC976ABCCB20B091292BFF4EA897EFCB639EA871CFA95F6DE339E4B0A",
		},
	}
	for _, v := range vectors {
		priv, err := parseKey(v.sk)
		if err != nil {
			t.Fatalf("parse key: %v", err)
		}
		pubKey := publicKey(priv)
		if got := hex.EncodeToString(pubKey[:]); !strings.EqualFold(got, v.pk) {
			t.Fatalf("public key = %s, want %s", got, v.pk)
		}
		aux := mustHex(t, v.aux)
		msg := mustHex(t, v.msg)
		var msg32 [32]byte
		copy(msg32[:], msg)
		sig, err := signSchnorr(priv, msg32, aux)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if got := hex.EncodeToString(sig[:]); !strings.EqualFold(got, v.sig) {
			t.Fatalf("signature = %s, want %s", got, v.sig)
		}
		var pub [32]byte
		pubBytes := mustHex(t, v.pk)
		copy(pub[:], pubBytes)
		if !verifySchnorr(pub, msg32, sig) {
			t.Fatal("signature failed verification")
		}
	}
	if _, err := parseKey("00"); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := parseKey("0000000000000000000000000000000000000000000000000000000000000000"); err == nil {
		t.Fatal("zero key accepted")
	}
}

// verifySchnorr is the independent test-side verifier; the adapter only signs.
func verifySchnorr(pub [32]byte, msg [32]byte, sig [64]byte) bool {
	p := new(big.Int).SetBytes(pub[:])
	pointPub, ok := liftX(p)
	if !ok {
		return false
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	n := curveN()
	if r.Cmp(curveP()) >= 0 || s.Cmp(n) >= 0 {
		return false
	}
	e := new(big.Int).SetBytes(taggedHashBytes("BIP0340/challenge", sig[:32], pub[:], msg[:]))
	e.Mod(e, n)
	left := scalarBaseMult(s)
	right := scalarMult(e, pointPub)
	neg := point{x: right.x, y: modP(new(big.Int).Neg(right.y))}
	sum := pointAdd(left, neg)
	if sum.inf || sum.y.Bit(0) == 1 {
		return false
	}
	return sum.x.Cmp(r) == 0
}

func scalarMult(k *big.Int, p point) point {
	result := point{inf: true}
	addend := p
	for i := range k.BitLen() {
		if k.Bit(i) == 1 {
			result = pointAdd(result, addend)
		}
		addend = pointDouble(addend)
	}
	return result
}

func liftX(x *big.Int) (point, bool) {
	p := curveP()
	if x.Cmp(p) >= 0 {
		return point{}, false
	}
	c := new(big.Int).Mul(x, x)
	c.Mul(c, x)
	c.Add(c, big.NewInt(7))
	c = modP(c)
	exp := new(big.Int).Add(p, big.NewInt(1))
	exp.Rsh(exp, 2)
	y := new(big.Int).Exp(c, exp, p)
	check := new(big.Int).Mul(y, y)
	if modP(check).Cmp(c) != 0 {
		return point{}, false
	}
	if y.Bit(0) == 1 {
		y = new(big.Int).Sub(p, y)
	}
	return point{x: x, y: y}, true
}
