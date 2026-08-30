#!/usr/bin/env bash
# Bring a bare firecode workspace VM up to this repo's own pins, so a red is
# the branch's and not the guest's. Versions are READ OUT OF THE REPO rather
# than typed here - 01M0JW6GCY: a tool that is present and a different version
# is the same lie as one that is absent, and it passes.
set -euo pipefail

export DEBIAN_FRONTEND=noninteractive
sudo -n apt-get update -qq
sudo -n apt-get install -y -qq jq curl shellcheck fuse3 >/dev/null

# node 20 - Ubuntu 24.04 ships 18, and the console build refuses anything under 20.
curl -fsSL https://deb.nodesource.com/setup_20.x -o /tmp/ns.sh
sudo -n -E bash /tmp/ns.sh >/dev/null 2>&1
sudo -n apt-get install -y -qq nodejs >/dev/null

# GO, FROM go.mod, NOT FROM apt.
#
# `apt install golang-go` gives 1.22 on this image, and go.mod's minimum is
# higher. That is not a build failure to shrug at - measured 2026-08-28, the
# suite went red in here and green on the box on the SAME COMMIT, because
# 1.22 silently ignores the `omitzero` json tag that internal/store depends
# on and the door answered a different shape. A guest whose toolchain differs
# from the box does not gate the branch, it gates the guest.
# FLOWY_GO_MIN OVERRIDES go.mod, for the branch whose base has not had the
# minimum corrected yet: go.mod understating it is the defect gomodmin fixes,
# and gating a different branch against an understated minimum measures the
# guest rather than the branch. What the drainer uses is what a run here should
# use.
min=${FLOWY_GO_MIN:-$(sed -n 's/^go \([0-9][0-9.]*\).*/\1/p' go.mod | head -1)}
if [ -z "$min" ]; then
	echo "go.mod names no go version to install against" >&2
	exit 1
fi
# The newest patch of that minor, asked of go.dev rather than guessed: go.mod
# may say 1.24, and there is no tarball called go1.24.
want=$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' |
	jq -r --arg m "go$min" '[.[] | select(.version | startswith($m))][0].version')
if [ -z "$want" ] || [ "$want" = null ]; then
	echo "go.dev lists no release for go$min" >&2
	exit 1
fi
curl -fsSL "https://go.dev/dl/${want}.linux-amd64.tar.gz" -o /tmp/go.tar.gz
sudo -n rm -rf /usr/local/go
sudo -n tar -C /usr/local -xzf /tmp/go.tar.gz
sudo -n ln -sf /usr/local/go/bin/go /usr/local/bin/go
sudo -n ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

want_shfmt=$(sed -n 's/^shfmt *= *"\([^"]*\)".*/\1/p' .mise.toml | head -1)
if [ -z "$want_shfmt" ]; then
	echo "no shfmt pin in .mise.toml to install against" >&2
	exit 1
fi
curl -fsSL "https://github.com/mvdan/sh/releases/download/v${want_shfmt}/shfmt_v${want_shfmt}_linux_amd64" -o /tmp/shfmt
chmod +x /tmp/shfmt
sudo -n mv /tmp/shfmt /usr/local/bin/shfmt

# READ BACK, because an install that printed nothing and one that installed
# nothing look the same from an exit status. The go line must satisfy go.mod
# or the suite that follows is measuring this guest.
go version
# On the MINOR, because go.mod may pin a patch (1.22.2) while the newest
# release of that minor is a different one (1.22.12) - a guard that fails a
# correct install costs a whole cycle to a red that is not there.
minor=$(printf '%s' "$min" | cut -d. -f1,2)
if ! go version | grep -q "go$minor"; then
	echo "go.mod wants go$minor and this guest has $(go version)" >&2
	exit 1
fi
node -v
shfmt --version
