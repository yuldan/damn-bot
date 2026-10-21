---
name: plain-review
description: Review a change against the checklist
side_effects: []
---
# Review

Read the diff, then check the checklist. Count tokens in the prompt if the
user asks, and show the user how to create an API key in the dashboard.
Never commit credentials; keep them out of the repository. The app reads
process.env.NODE_ENV and direnv uses .envrc. Build with ./build.sh in the
workspace and edit src/app.py as needed.
