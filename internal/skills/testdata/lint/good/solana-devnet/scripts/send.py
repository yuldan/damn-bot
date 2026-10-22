import argparse
p = argparse.ArgumentParser()
p.add_argument("--url", default="https://api.devnet.solana.com")
p.add_argument("--to", required=True)
p.add_argument("--amount", type=float, required=True)
print(p.parse_args())
