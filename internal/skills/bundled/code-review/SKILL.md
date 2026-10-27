---
name: code-review
description: Review a change for correctness, tests, and regressions before it is committed.
---
# Code review

Use this skill when the user asks for a review of a diff, branch, or pull request.

## Procedure

1. Read the change and the surrounding code before commenting. State the file and line you are discussing.
2. Check correctness first: error paths, boundary values, cancellation, and partial failure. A test that only covers the happy path is not enough.
3. Check that every new behavior has a test, and that the test fails when the behavior is removed.
4. Look for regressions in callers: signatures, persisted data, configuration defaults, and platform-specific paths.
5. Report findings in severity order. Separate blocking defects from optional polish, and say when you are unsure instead of guessing.

## Limits

- Do not rewrite the change unless the user asks for a patch.
- Do not approve on the strength of a passing test run alone; read what the test asserts.
- Keep secrets, credentials, and personal data out of comments and examples.
