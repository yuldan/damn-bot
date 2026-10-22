---
name: with-script
description: Lint the project with the bundled scripts
requires: [plain-review]
side_effects: [write]
---
Run `bash scripts/check.sh` from the workspace root, then
`python3 ${SKILL_DIR}/scripts/lint.py --fix`. See skill://plain-review for
the review steps.

Then run `python3 "$SKILL_DIR"/scripts/lint.py --fix`.
