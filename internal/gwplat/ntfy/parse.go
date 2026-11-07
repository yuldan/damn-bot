package ntfy

import "encoding/json"

// message is the ntfy JSON object delivered on the SSE stream.
type message struct {
	ID      string `json:"id"`
	Time    string `json:"time"`
	Event   string `json:"event"`
	Message string `json:"message"`
	Topic   string `json:"topic"`
}

// parseMessage accepts only message events with content; keepalive/open events
// are dropped.
func parseMessage(data []byte) (message, bool) {
	var msg message
	if err := json.Unmarshal(data, &msg); err != nil {
		return message{}, false
	}
	if msg.Event != "" && msg.Event != "message" {
		return message{}, false
	}
	if msg.Message == "" {
		return message{}, false
	}
	return msg, true
}
