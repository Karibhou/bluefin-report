#!/bin/bash
# Launch Bluefin Report with the Homebrew environment so gh is on PATH.
BREW_PATH="/home/linuxbrew/.linuxbrew/bin/brew"
if [ -f "$BREW_PATH" ]; then
    eval "$($BREW_PATH shellenv)"
fi
exec bluefin-report "$@"
