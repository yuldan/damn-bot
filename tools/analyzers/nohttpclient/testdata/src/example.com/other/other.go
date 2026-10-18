// Package other is outside the damnbot module and is never checked.
package other

import "net/http"

var _ = &http.Client{}

var _ = http.DefaultClient
