package gateway

// Action is a gateway capability whose authorization depends on the role.
// The list is closed: any value not listed here is denied for every role.
type Action string

// Owner-only actions (SPEC §9.7: dangerous tools and configuration are owner
// only). Pairing/allowlist changes and access-policy changes are the two this
// slice authorizes; chat commands that need role checks are added with their
// implementation.
const (
	ActionPair   Action = "pair"   // create/consume a pairing code, change the allowlist
	ActionPolicy Action = "policy" // change the gateway access policy or lock
)

// Allows reports whether the ticket may perform a. Only an owner may perform
// an owner action; an unknown action is denied for everyone.
func (t *Ticket) Allows(a Action) bool {
	return allows(t.Identity.Role, a)
}
