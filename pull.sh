#!/usr/bin/env bash
#
# pull.sh - pull the latest changes from GitHub into this working copy on the
# Raspberry Pi CM4 (Debian arm64).
#
# The remote is HTTPS, so a Personal Access Token (PAT) is read from a LOCAL
# file that never leaves the Pi. Resolution order:
#   1. --token-file <path>
#   2. $IEC104_GIT_TOKEN_FILE
#   3. $HOME/.iec104-git-token          (recommended: outside the repo)
# The file must contain the token on a single line (CR/LF/whitespace trimmed)
# and should be chmod 600. The token is never written to .git/config, never
# echoed, and build artifacts (dist/) and logs/ are gitignored so a pull never
# clobbers them.
#
# The repository defaults to github.com/Mhd25OrCode/IEC104_Go; export
# IEC104_GIT_REPO_URL to pull from somewhere else (e.g. a local mirror). Only
# an https:// URL needs a token.
#
# Usage:  ./pull.sh [--branch main] [--token-file FILE] [--stash] [--dry-run]
#                   [--clone] [--force]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

REPO_URL="${IEC104_GIT_REPO_URL:-https://github.com/Mhd25OrCode/IEC104_Go.git}"
BRANCH="main"
TOKEN_FILE="${IEC104_GIT_TOKEN_FILE:-$HOME/.iec104-git-token}"
STASH=0; DRY=0; CLONE=0; FORCE=0

while [[ $# -gt 0 ]]; do
	case "$1" in
		--branch)     BRANCH="${2:?--branch needs a value}";         shift 2 ;;
		--token-file) TOKEN_FILE="${2:?--token-file needs a value}"; shift 2 ;;
		--stash)      STASH=1; shift ;;
		--dry-run)    DRY=1;   shift ;;
		--clone)      CLONE=1; shift ;;
		--force)      FORCE=1; shift ;;
		-h|--help)    sed -n '2,21p' "$0"; exit 0 ;;
		*) echo "error: unknown argument: $1" >&2; exit 2 ;;
	esac
done

# Normalise the token path: a relative path is relative to the repo root (the
# script cd'd there first), so the "inside the working copy" warning can fire.
if [[ "$TOKEN_FILE" != /* ]]; then
	TOKEN_FILE="$ROOT/$TOKEN_FILE"
fi
if [[ -f "$TOKEN_FILE" ]]; then
	TOKEN_FILE="$(cd "$(dirname "$TOKEN_FILE")" && pwd)/$(basename "$TOKEN_FILE")"
fi

# --- authentication ------------------------------------------------------
# A token is only required for an https:// remote (the default GitHub URL). A
# file:// mirror or an ssh:// remote authenticates by other means.
if [[ "$REPO_URL" == https://* ]]; then
	TOKEN=""
	if [[ -f "$TOKEN_FILE" ]]; then
		TOKEN="$(tr -d ' \t\r\n' < "$TOKEN_FILE")"
	fi

	if [[ "$TOKEN_FILE" == "$ROOT"/* ]]; then
		echo "warning: token file $TOKEN_FILE is inside the working copy -" >&2
		echo "         keep it in \$HOME instead so it can never be pushed" >&2
	fi

	if [[ -n "$TOKEN" && -f "$TOKEN_FILE" ]]; then
		PERM="$(stat -c '%a' "$TOKEN_FILE" 2>/dev/null || echo '')"
		if [[ -n "$PERM" && "$PERM" != "600" ]]; then
			echo ">> tightening $TOKEN_FILE from $PERM to 600"
			chmod 600 "$TOKEN_FILE" || true
		fi
	fi

	if [[ -z "$TOKEN" ]]; then
		echo "error: no token found in $TOKEN_FILE" >&2
		echo "hint:  read -rsp 'PAT: ' T; printf '%s\\n' \"\$T\" > $TOKEN_FILE; chmod 600 $TOKEN_FILE" >&2
		echo "       or pass --token-file <path>, or export IEC104_GIT_TOKEN_FILE=<path>" >&2
		exit 1
	fi

	AUTH_URL="https://x-access-token:${TOKEN}@${REPO_URL#https://}"
else
	AUTH_URL="$REPO_URL"
fi

redact() { sed -E 's#https://[^@/]+@#https://***@#g'; }

# --- bootstrap (first run on the Pi, empty directory) --------------------
if ! git rev-parse --git-dir >/dev/null 2>&1; then
	if [[ "$CLONE" -eq 1 ]]; then
		# "git clone" requires an empty target, but this script already lives
		# in $ROOT, so bootstrap in place: init + fetch + checkout. origin is
		# set to the token-free URL, so no secret is stored in .git/config.
		echo ">> initialising $ROOT and fetching $BRANCH"
		git init -q -b "$BRANCH" "$ROOT"
		git -C "$ROOT" remote add origin "$REPO_URL" 2>/dev/null ||
			git -C "$ROOT" remote set-url origin "$REPO_URL"
		git -C "$ROOT" fetch --prune "$AUTH_URL" "$BRANCH" 2> >(redact >&2)
		git -C "$ROOT" checkout -q -B "$BRANCH" FETCH_HEAD
		echo ">> done - next: ./build.sh"
		exit 0
	fi
	echo "error: $ROOT is not a git working copy - re-run with --clone" >&2
	exit 1
fi

# --- dirty-tree guard (tracked files only; dist/ + logs/ are gitignored) --
DIRTY="$(git status --porcelain --untracked-files=no)"
if [[ -n "$DIRTY" ]]; then
	if [[ "$STASH" -eq 1 ]]; then
		echo ">> stashing local changes"
		git stash push --message "pull.sh auto-stash $(date -Is)"
	elif [[ "$FORCE" -eq 1 ]]; then
		echo ">> --force: discarding local changes to tracked files"
		git checkout -- .
	else
		echo "error: local tracked changes present - commit them, or re-run with" >&2
		echo "       --stash to keep them / --force to discard them:" >&2
		echo "$DIRTY" >&2
		exit 1
	fi
fi

BEFORE="$(git rev-parse --short HEAD)"
echo ">> current revision: $BEFORE (branch $(git rev-parse --abbrev-ref HEAD))"

if [[ "$DRY" -eq 1 ]]; then
	echo ">> dry run - not fetching"
	exit 0
fi

if [[ "$REPO_URL" == https://* ]]; then
	echo ">> fetching $BRANCH from $REPO_URL (token from $TOKEN_FILE)"
else
	echo ">> fetching $BRANCH from $REPO_URL"
fi
if ! git fetch --prune "$AUTH_URL" "$BRANCH" 2> >(redact >&2); then
	echo "error: fetch failed - check the token in $TOKEN_FILE and the network" >&2
	exit 1
fi

echo ">> fast-forwarding to FETCH_HEAD"
if ! git merge --ff-only FETCH_HEAD; then
	echo "error: not a fast-forward (upstream rewrote history?)" >&2
	echo "       inspect: git --no-pager log --oneline --graph HEAD...FETCH_HEAD" >&2
	exit 1
fi

AFTER="$(git rev-parse --short HEAD)"
if [[ "$BEFORE" == "$AFTER" ]]; then
	echo ">> already up to date ($AFTER)"
else
	echo ">> updated $BEFORE -> $AFTER"
	git --no-pager log --oneline "$BEFORE..$AFTER"
fi

echo ">> next: ./build.sh && sudo ./deploy/install.sh"