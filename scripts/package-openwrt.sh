#!/usr/bin/env bash
# Produce the opkg package and build the native apk through the matching SDK.
set -euo pipefail

binary=$1
architecture=$2
version=$3
sdk=$4
case "$architecture" in
  x86_64|aarch64_cortex-a53) ;;
  *) echo "Unsupported OpenWrt architecture: $architecture" >&2; exit 1 ;;
esac
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p dist "$work/control" "$work/data/usr/bin" "$work/data/etc/agent-core"
install -m 0755 "$binary" "$work/data/usr/bin/agent-core"
install -m 0600 agent-core.yaml "$work/data/etc/agent-core/agent-core.yaml.example"
cat > "$work/control/control" <<CONTROL
Package: agent-core
Version: ${version}-1
Architecture: ${architecture}
Maintainer: AgentCoreGo contributors
Section: utils
Priority: optional
Depends: ca-bundle
Description: Self-hosted Go AI agent server.
CONTROL

printf '2.0\n' > "$work/debian-binary"
tar -C "$work/control" --owner=0 --group=0 --numeric-owner -czf "$work/control.tar.gz" .
tar -C "$work/data" --owner=0 --group=0 --numeric-owner -czf "$work/data.tar.gz" .
ipk="dist/agent-core_${version}-1_${architecture}.ipk"
tar -C "$work" --owner=0 --group=0 --numeric-owner -czf "$ipk" ./debian-binary ./control.tar.gz ./data.tar.gz
tar -tzf "$ipk"

package="$sdk/package/agent-core"
mkdir -p "$package/files"
cp packaging/openwrt/Makefile "$package/"
cp "$binary" "$package/files/agent-core"
cp agent-core.yaml "$package/files/agent-core.yaml.example"
printf '%s\n' 'CONFIG_PACKAGE_agent-core=m' >> "$sdk/.config"
make -C "$sdk" defconfig PKG_VERSION="$version"
make -C "$sdk" package/agent-core/compile V=s PKG_VERSION="$version"
apk=$(find "$sdk/bin/packages" -name 'agent-core-*.apk' -type f -print -quit)
test -n "$apk" && test -s "$apk"
output="dist/agent-core-${version}-r1_openwrt-25.12.5_${architecture}.apk"
cp "$apk" "$output"
apk_tool=$(find "$sdk/staging_dir/host/bin" -name apk -type f -print -quit)
test -x "$apk_tool"
"$apk_tool" adbdump "$output"
