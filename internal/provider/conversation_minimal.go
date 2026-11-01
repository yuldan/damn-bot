//go:build minimal

package provider

// Turn aliases Message in the minimal build. Minimal links only the
// openai-chat adapter, so there is no second mapping to diverge from and the
// shared validation is not linked; the chat encoder keeps its F1 behaviour
// and the minimal binary does not grow (E-60/P13).
type Turn = Message

// Conversation returns msgs unchanged in the minimal build.
func Conversation(msgs []Message) ([]Turn, error) { return msgs, nil }
