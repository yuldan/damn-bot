# Surface parity

<!-- Code generated from cmd/damnbot/surface_parity.go; DO NOT EDIT. -->

Server requests × frontends. A `fallback` cell names the defined
alternative, so no frontend silently answers `unsupported_command`.

| Request | tui | web | acp | gateway | headless |
|---|---|---|---|---|---|
| `approval` | render | render | render | render | fallback: `reject:approval_required` |
| `ask_user` | render | render | fallback: `reject:ask_user_unavailable` | render | fallback: `reject:ask_user_unavailable` |
| `secret_input` | render | fallback: `reject:secret_input_local_only` | fallback: `reject:secret_input_local_only` | fallback: `reject:secret_input_local_only` | fallback: `reject:secret_input_local_only` |
| `plan_review` | render | render | render | render | fallback: `reject:plan_review_unavailable` |
| `budget_raise` | render | render | render | render | fallback: `forward:owner` |
| `undo_confirm` | render | render | fallback: `reject:undo_confirm_unavailable` | render | fallback: `reject:undo_confirm_unavailable` |

## Stream updates

Every frontend renders or has a defined fallback for the closed
update vocabulary: `notice`, `stop`, `text_delta`, `tool_call_args`, `tool_call_end`, `tool_call_start`, `tool_result`, `usage`.
