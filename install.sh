#!/bin/sh
# WrongTrace installer for Linux and macOS.
#
#   curl -fsSL https://raw.githubusercontent.com/WrongStack/WrongTrace/master/install.sh | sh
#
# Environment overrides:
#   WRONGTRACE_VERSION      release tag to install (default: latest), e.g. v0.3.17
#   WRONGTRACE_INSTALL_DIR  target directory (default: $HOME/.local/bin)
#   WRONGTRACE_NO_MODIFY_PATH=1  never touch shell profile files
#
# Flags: --version <tag>, --dir <path>, --no-modify-path, --uninstall
set -eu

REPO_URL="https://github.com/WrongStack/WrongTrace"
VERSION="${WRONGTRACE_VERSION:-}"
INSTALL_DIR="${WRONGTRACE_INSTALL_DIR:-$HOME/.local/bin}"
NO_MODIFY_PATH="${WRONGTRACE_NO_MODIFY_PATH:-0}"
UNINSTALL=0

info() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
		--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift 2 ;;
		--dir) [ $# -ge 2 ] || die "--dir needs a value"; INSTALL_DIR="$2"; shift 2 ;;
		--no-modify-path) NO_MODIFY_PATH=1; shift ;;
		--uninstall) UNINSTALL=1; shift ;;
		-h|--help) sed -n '2,13p' "$0" 2>/dev/null || true; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

BIN="$INSTALL_DIR/wrongtrace"

if [ "$UNINSTALL" = 1 ]; then
	if [ -e "$BIN" ]; then
		rm -f "$BIN"
		info "Removed $BIN"
	else
		info "Nothing to remove at $BIN"
	fi
	info "Data in \${WRONGTRACE_HOME:-~/.wrongtrace} was left in place; delete it manually if unwanted."
	exit 0
fi

# --- HTTP helpers ------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
	resolve_latest() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$REPO_URL/releases/latest"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
	resolve_latest() {
		wget -S --spider "$REPO_URL/releases/latest" 2>&1 \
			| sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'
	}
else
	die "curl or wget is required"
fi

# --- Platform ------------------------------------------------------------------
case "$(uname -s)" in
	Linux) OS=linux ;;
	Darwin) OS=darwin ;;
	*) die "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
	x86_64|amd64) ARCH=amd64 ;;
	arm64|aarch64) ARCH=arm64 ;;
	*) die "unsupported architecture $(uname -m)" ;;
esac
# Rosetta: an x86_64 shell on Apple Silicon should still get the native build.
if [ "$OS" = darwin ] && [ "$ARCH" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	ARCH=arm64
fi
case "$OS-$ARCH" in
	linux-amd64|darwin-amd64|darwin-arm64) ;;
	*) die "no prebuilt binary for $OS-$ARCH; build from source: $REPO_URL#building-from-source" ;;
esac

# --- Version -------------------------------------------------------------------
if [ -z "$VERSION" ]; then
	info "Resolving latest release"
	LATEST_URL="$(resolve_latest)" || die "could not reach $REPO_URL"
	VERSION="${LATEST_URL##*/tag/}"
	[ -n "$VERSION" ] && [ "$VERSION" != "$LATEST_URL" ] || die "could not determine the latest release (got $LATEST_URL)"
fi
case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac

ASSET="wrongtrace-$VERSION-$OS-$ARCH"
BASE="$REPO_URL/releases/download/$VERSION"

TMP="$(mktemp -d 2>/dev/null || mktemp -d -t wrongtrace)"
trap 'rm -rf "$TMP"' EXIT INT TERM

info "Downloading WrongTrace $VERSION ($OS-$ARCH)"
fetch "$BASE/$ASSET" "$TMP/wrongtrace" || die "download failed: $BASE/$ASSET"

# --- Checksum --------------------------------------------------------------------
if fetch "$BASE/checksums.txt" "$TMP/checksums.txt" 2>/dev/null; then
	EXPECTED="$(awk -v a="$ASSET" '{ f=$2; sub(/^\*/, "", f); if (f == a) print $1 }' "$TMP/checksums.txt")"
	[ -n "$EXPECTED" ] || die "$ASSET is not listed in checksums.txt"
	if command -v sha256sum >/dev/null 2>&1; then
		ACTUAL="$(sha256sum "$TMP/wrongtrace" | awk '{print $1}')"
	elif command -v shasum >/dev/null 2>&1; then
		ACTUAL="$(shasum -a 256 "$TMP/wrongtrace" | awk '{print $1}')"
	else
		ACTUAL=""
		warn "no sha256sum/shasum found; skipping checksum verification"
	fi
	if [ -n "$ACTUAL" ]; then
		[ "$ACTUAL" = "$EXPECTED" ] || die "checksum mismatch (expected $EXPECTED, got $ACTUAL)"
		info "Checksum verified"
	fi
else
	warn "release $VERSION publishes no checksums.txt; skipping checksum verification"
fi

# --- Install ---------------------------------------------------------------------
chmod 0755 "$TMP/wrongtrace"
# Downloaded files are quarantined on macOS; strip it so Gatekeeper does not block the first run.
if [ "$OS" = darwin ]; then
	xattr -d com.apple.quarantine "$TMP/wrongtrace" 2>/dev/null || true
fi
"$TMP/wrongtrace" --version >/dev/null 2>&1 || die "downloaded binary does not run on this system"

mkdir -p "$INSTALL_DIR" 2>/dev/null || die "cannot create $INSTALL_DIR (try --dir or sudo)"
[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable (try --dir <path> or run with sudo)"
# mv over the old file is atomic and safe even while a daemon is running.
cp "$TMP/wrongtrace" "$INSTALL_DIR/.wrongtrace.new"
mv -f "$INSTALL_DIR/.wrongtrace.new" "$BIN"
info "Installed $("$BIN" --version 2>/dev/null || echo wrongtrace) to $BIN"

# --- PATH ------------------------------------------------------------------------
case ":$PATH:" in
	*":$INSTALL_DIR:"*) ON_PATH=1 ;;
	*) ON_PATH=0 ;;
esac

if [ "$ON_PATH" = 0 ]; then
	if [ "$NO_MODIFY_PATH" = 1 ]; then
		warn "$INSTALL_DIR is not on your PATH; add it manually."
	else
		LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
		PROFILES=""
		case "$(basename "${SHELL:-sh}")" in
			zsh) PROFILES="$HOME/.zshrc" ;;
			bash)
				PROFILES="$HOME/.bashrc"
				# macOS login shells read .bash_profile, not .bashrc.
				[ "$OS" = darwin ] && PROFILES="$PROFILES $HOME/.bash_profile"
				;;
			fish) PROFILES="" ;;
			*) PROFILES="$HOME/.profile" ;;
		esac

		if [ "$(basename "${SHELL:-sh}")" = fish ]; then
			FISH_CONF="$HOME/.config/fish/conf.d/wrongtrace.fish"
			mkdir -p "$(dirname "$FISH_CONF")"
			echo "fish_add_path -g \"$INSTALL_DIR\"" >"$FISH_CONF"
			info "Added $INSTALL_DIR to PATH in $FISH_CONF"
		fi
		for p in $PROFILES; do
			if [ -f "$p" ] && grep -F "$INSTALL_DIR" "$p" >/dev/null 2>&1; then
				continue
			fi
			printf '\n# Added by the WrongTrace installer\n%s\n' "$LINE" >>"$p"
			info "Added $INSTALL_DIR to PATH in $p"
		done
		echo
		info "Open a new terminal, or run this to use wrongtrace right away:"
		echo "    $LINE"
	fi
fi

echo
info "Done. Get started with:"
echo "    wrongtrace init     # agent rules, .mcp.json, git hooks"
echo "    wrongtrace start    # daemon + dashboard on http://localhost:3444"
echo "    wrongtrace update   # upgrade to the latest release later"
