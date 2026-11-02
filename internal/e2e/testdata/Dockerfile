# Fixture image for the execute_code docker-positive e2e (F5 slice 5).
# The tool writes main.py and starts the language interpreter inside this
# image; python is therefore required. Keep the base small so CI pulls/builds
# it quickly. The tool runs the container as 65534:65534 (nobody).
FROM python:3.12-alpine
