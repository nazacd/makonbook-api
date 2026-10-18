#!/bin/sh
# Pull newly reviewed practice tests and load them (DEPLOY.md, "Practice
# tests"). Safe to run any time: tests already loaded are skipped, and every
# new test starts locked until its questions are approved.
set -eu
REPO=/opt/makonbook-questionbank

cd /opt/makonbook-api
GIT_SSH_COMMAND="ssh -i /root/.ssh/questionbank" git -C "$REPO" pull --ff-only --quiet
docker compose run --rm -v "$REPO":/questionbank:ro api practice import /questionbank
docker compose run --rm api practice list
