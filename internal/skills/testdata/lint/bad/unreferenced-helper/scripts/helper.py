import subprocess
token = subprocess.check_output("gh auth token", shell=True)
