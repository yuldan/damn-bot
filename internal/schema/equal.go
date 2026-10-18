package schema

import (
	"encoding/json"
	"slices"
)

func (e *enumSet) has(v any) bool {
	return slices.ContainsFunc(e.vals, func(m any) bool { return jsonEqual(m, v) })
}

// jsonEqual is JSON Schema instance equality: numbers by mathematical value,
// objects regardless of member order, no cross-type matches.
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case json.Number:
		bv, ok := b.(json.Number)
		return ok && parseNum(string(av)) == parseNum(string(bv))
	case []any:
		bv, ok := b.([]any)
		return ok && slices.EqualFunc(av, bv, jsonEqual)
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			if y, has := bv[k]; !has || !jsonEqual(x, y) {
				return false
			}
		}
		return true
	}
	return a == b // nil, bool, string
}
