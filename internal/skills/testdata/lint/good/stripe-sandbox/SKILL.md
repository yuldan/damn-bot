---
name: stripe-sandbox
description: Create Stripe test-mode payment intents
side_effects: [payment, network]
---
Use the Stripe CLI in test mode: `stripe payment_intents create --amount 100
--currency usd`. The STRIPE_API_KEY is injected by exec.env; never ask for it.
