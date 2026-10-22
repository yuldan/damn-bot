import subprocess, sys
sys.exit(subprocess.call(["gofmt", "-l", "."]))
