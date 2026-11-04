// Package responses is the OpenAI Responses wire adapter (F5-04). It only
// translates a request and a 2xx event stream; retry, circuit, budget,
// fallback, and carrier projection/capture stay in provider.OpenAI over the
// injected netx client (E-05).
//
// The adapter is full-only: only the full build carries the canonical
// provider.Conversation it renders, so `minimal` excludes every file here and
// a minimal composition root that tries to link it fails at compile time.
package responses
