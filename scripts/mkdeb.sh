#!/bin/sh
set -e

# mkdeb.sh - build hostit_<version>_linux_<arch>.deb with plain dpkg-deb.
# goreleaser (see .goreleaser.yml) is the proper release path, but it requires
# a git repository; this script produces an identical-layout package from a
# plain source tree with no tooling beyond Go and dpkg-deb.
#
# Usage: scripts/mkdeb.sh [version] [arch]

VERSION="${1:-0.1.0}"
ARCH="${2:-amd64}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGE="$ROOT/dist/deb_$ARCH"
OUT="$ROOT/dist/hostit_${VERSION}_linux_${ARCH}.deb"

rm -rf "$STAGE"
mkdir -p "$STAGE/DEBIAN" "$STAGE/usr/bin" "$STAGE/etc/hostit" "$STAGE/etc/sudoers.d" "$STAGE/lib/systemd/system"

# Build the binary (static, no cgo; matches the goreleaser build flags)
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -C "$ROOT" -trimpath \
  -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/usr/bin/hostit" .

# Package contents, mirroring the nfpm section of .goreleaser.yml
install -m 644 "$ROOT/server.yml.example" "$STAGE/etc/hostit/server.yml.example"
install -m 644 "$ROOT/hostit.service" "$STAGE/lib/systemd/system/hostit.service"
install -m 755 "$ROOT/hostit-shell" "$STAGE/usr/bin/hostit-shell"
install -m 755 "$ROOT/hostit-enter" "$STAGE/usr/bin/hostit-enter"
install -m 644 "$ROOT/hostit-app@.service" "$STAGE/lib/systemd/system/hostit-app@.service"
install -m 440 "$ROOT/hostit.sudoers" "$STAGE/etc/sudoers.d/hostit"
install -m 755 "$ROOT/scripts/postinst.sh" "$STAGE/DEBIAN/postinst"
install -m 755 "$ROOT/scripts/postrm.sh" "$STAGE/DEBIAN/postrm"

cat > "$STAGE/DEBIAN/control" <<EOF
Package: hostit
Version: $VERSION
Section: net
Priority: optional
Architecture: $ARCH
Maintainer: Philipp C. Heckel <phil@heckel.io>
Depends: openssh-server, podman, uidmap, slirp4netns, nftables, dbus-user-session
Recommends: passt
Description: Self-hosted mini-app platform with SSH access, subdomains and TLS
 hostit runs isolated mini apps as Unix users behind a TLS-terminating
 reverse proxy. Apps are created via a REST API and deployed over SSH
 with "hostit up"; each runs in its own podman container with a per-app
 uid mapping.
EOF

# Normalize permissions (umask leaks 775 into the staging tree otherwise)
find "$STAGE" -type d -exec chmod 755 {} +
chmod 755 "$STAGE/usr/bin/hostit"

dpkg-deb --build --root-owner-group "$STAGE" "$OUT" >/dev/null
rm -rf "$STAGE"
echo "$OUT"
