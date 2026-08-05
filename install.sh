#!/bin/sh
# Install deepseek-cli and its aliases.
#
#   curl -sL https://raw.githubusercontent.com/thevibeworks/deepseek-cli/main/install.sh | sh
#
# Downloads the latest release for this platform, installs the binary to
# PREFIX/bin (default /usr/local/bin), and symlinks `ds` and `dscli` to
# it — the binary answers to whichever name invoked it.
set -eu

REPO=thevibeworks/deepseek-cli
PREFIX=${PREFIX:-/usr/local}
BINDIR="$PREFIX/bin"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
case "$os" in
    linux | darwin) ;;
    *) echo "unsupported OS: $os — see https://github.com/$REPO/releases" >&2; exit 1 ;;
esac

asset="deepseek_${os}_${arch}.tar.gz"
url="https://github.com/$REPO/releases/latest/download/$asset"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading $asset"
curl -fsSL "$url" | tar xz -C "$tmp"

# Only escalate if the target is not writable by this user.
sudo=""
if [ ! -w "$BINDIR" ]; then
    if command -v sudo >/dev/null 2>&1; then
        sudo=sudo
    else
        echo "$BINDIR is not writable and sudo is unavailable; set PREFIX=\$HOME/.local" >&2
        exit 1
    fi
fi

$sudo mkdir -p "$BINDIR"
$sudo install -m 0755 "$tmp/deepseek" "$BINDIR/deepseek"
for alias in ds dscli; do
    $sudo ln -sf deepseek "$BINDIR/$alias"
done

echo "installed: $BINDIR/deepseek (aliases: ds, dscli)"
"$BINDIR/deepseek" --version
echo
echo "next: export DEEPSEEK_API_KEY=sk-...  &&  ds check"
