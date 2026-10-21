---
name: solana-devnet
description: Send SOL on devnet for testing
side_effects: [payment]
---
Run `python3 scripts/send.py --to <address> --amount 0.1`. The script uses
devnet unless the user passes another cluster URL explicitly.
