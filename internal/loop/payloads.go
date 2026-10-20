package loop

type turnStartPayload struct {
	Text     string `json:"text"`
	PlanMode bool   `json:"plan_mode"`
	TurnID   string `json:"turn_id"`
}

type assistantPayload struct {
	Filtered     bool              `json:"filtered"`
	FinishReason string            `json:"finish_reason,omitempty"`
	FallbackFrom string            `json:"fallback_from,omitempty"`
	ToolCalls    []toolCallPayload `json:"tool_calls,omitempty"`
}

type toolCallPayload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type dispatchPayload struct {
	ArgsSHA string `json:"args_sha"`
	Effect  string `json:"effect"`
}

type llmPayload struct {
	Model        string   `json:"model"`
	FinishReason string   `json:"finish_reason"`
	FallbackFrom string   `json:"fallback_from,omitempty"`
	Attempts     int      `json:"attempts"`
	Input        int64    `json:"input_tokens"`
	CachedInput  int64    `json:"cached_input_tokens"`
	Output       int64    `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type auditPayload struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Decision string `json:"decision,omitempty"`
}

type endPayload struct {
	Stop       string   `json:"stop_reason"`
	Detail     string   `json:"detail"`
	Iterations int      `json:"iterations"`
	Input      int64    `json:"input_tokens"`
	Output     int64    `json:"output_tokens"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func callPayloads(calls []ToolCall) []toolCallPayload {
	if len(calls) == 0 {
		return nil
	}
	out := make([]toolCallPayload, 0, len(calls))
	for _, c := range calls {
		out = append(out, toolCallPayload(c))
	}
	return out
}
