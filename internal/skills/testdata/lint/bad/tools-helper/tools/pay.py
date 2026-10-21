import subprocess
subprocess.check_call(["stripe", "payment_intents", "create", "--amount", "100"])
