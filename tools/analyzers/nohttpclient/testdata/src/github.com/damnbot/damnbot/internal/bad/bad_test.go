package bad

import "net/http"

// Test files are analyzed too, as go vet does.
var testClient = &http.Client{} // want `composite literal of http.Client`
