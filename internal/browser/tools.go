package browser

import "encoding/json"

// Tools returns the module's deferred tools in stable order: the three reads
// first, then the two approval-gated page actions.
func Tools() []Tool {
	return []Tool{
		newTool("browser_navigate", "Open one allowlisted http(s) URL in the page", "read", `{"type":"object","properties":{"url":{"type":"string","maxLength":2048}},"required":["url"],"additionalProperties":false}`),
		newTool("browser_eval", "Evaluate a JavaScript expression in the current page and return its JSON value", "read", `{"type":"object","properties":{"expression":{"type":"string","maxLength":4096}},"required":["expression"],"additionalProperties":false}`),
		newTool("browser_screenshot", "Capture the current viewport as a PNG", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_snapshot", "Return the accessibility tree with stable element refs as JSON", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_wait_for", "Wait until a JavaScript expression is truthy or the timeout expires", "read", `{"type":"object","properties":{"expression":{"type":"string","maxLength":4096},"timeout_ms":{"type":"integer","minimum":0,"maximum":30000}},"required":["expression"],"additionalProperties":false}`),
		newTool("browser_pdf", "Render the current page as PDF", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_click", "Click the current page at viewport coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}`),
		newTool("browser_type", "Insert text into the focused element after approval", "non_idempotent", `{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`),
		newTool("browser_back", "Go back one history entry after approval", "non_idempotent", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_scroll", "Scroll the page at coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"},"delta_x":{"type":"number","minimum":-10000,"maximum":10000},"delta_y":{"type":"number","minimum":-10000,"maximum":10000}},"required":["delta_x","delta_y"],"additionalProperties":false}`),
		newTool("browser_press", "Press one keyboard key after approval", "non_idempotent", `{"type":"object","properties":{"key":{"type":"string","maxLength":32}},"required":["key"],"additionalProperties":false}`),
		newTool("browser_hover", "Move the pointer to viewport coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}},"required":["x","y"],"additionalProperties":false}`),
		newTool("browser_drag", "Drag from one viewport point to another after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"},"end_x":{"type":"number"},"end_y":{"type":"number"}},"required":["x","y","end_x","end_y"],"additionalProperties":false}`),
		newTool("browser_select", "Set a control value by selector after approval", "non_idempotent", `{"type":"object","properties":{"selector":{"type":"string","maxLength":1024},"value":{"type":"string","maxLength":4096}},"required":["selector","value"],"additionalProperties":false}`),
		newTool("browser_upload", "Set a file input from a workspace file after approval", "non_idempotent", `{"type":"object","properties":{"selector":{"type":"string","maxLength":1024},"path":{"type":"string","maxLength":1024}},"required":["selector","path"],"additionalProperties":false}`),
		newTool("browser_download", "Download an allowlisted URL into a workspace file after approval", "non_idempotent", `{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"path":{"type":"string","maxLength":1024}},"required":["url","path"],"additionalProperties":false}`),
	}
}

func newTool(name, description, effect, schema string) Tool {
	return Tool{Name: name, Description: description, Effect: effect, Schema: json.RawMessage(schema)}
}
