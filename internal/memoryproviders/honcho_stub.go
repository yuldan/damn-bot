package memoryproviders

import "fmt"

// newHoncho is replaced by the Honcho implementation slice. Until then the
// name stays in the closed registry but opening it fails closed.
func newHoncho(*Settings) (Provider, error) {
	return nil, fmt.Errorf("%w: honcho implementation is not linked", ErrUnavailable)
}
