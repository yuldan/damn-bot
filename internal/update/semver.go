package update

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type semver struct {
	nums [3]int
	pre  string
	has  bool
}

func parseSemver(s string) (semver, bool) {
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, has := strings.Cut(s, "-")
	nums, ok := parseNums(core)
	if !ok || has && (pre == "" || strings.IndexFunc(pre, invalidPreRune) >= 0) {
		return semver{}, false
	}
	return semver{nums: nums, pre: pre, has: has}, true
}

func parseNums(core string) ([3]int, bool) {
	var nums [3]int
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return nums, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return nums, false
		}
		nums[i] = n
	}
	return nums, true
}

func invalidPreRune(r rune) bool {
	return r != '-' && r != '.' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
}

func compareSemver(a, b semver) int {
	if c := slices.Compare(a.nums[:], b.nums[:]); c != 0 {
		return c
	}
	switch {
	case !a.has && !b.has:
		return 0
	case !a.has:
		return 1
	case !b.has:
		return -1
	}
	ai, bi := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(ai) && i < len(bi); i++ {
		if c := compareIdentifier(ai[i], bi[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(ai), len(bi))
}

func compareIdentifier(a, b string) int {
	an, aerr := strconv.Atoi(a)
	bn, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		return cmp.Compare(an, bn)
	case aerr == nil:
		return -1
	case berr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func newerThan(target, current string) (bool, error) {
	t, ok := parseSemver(target)
	if !ok {
		return false, fmt.Errorf("update: target version %q is not semver", target)
	}
	c, ok := parseSemver(current)
	if !ok {
		return true, nil
	}
	switch c := compareSemver(t, c); {
	case c < 0:
		return false, fmt.Errorf("update: target %s is older than installed %s; use --rollback", target, current)
	default:
		return c > 0, nil
	}
}
