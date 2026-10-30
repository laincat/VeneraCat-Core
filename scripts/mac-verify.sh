#!/bin/sh
# Mac-side build and measurement for VeneraCat Core. Run from the repository
# root on macOS with Xcode. Prints a machine-readable summary at the end.
#
#   ./scripts/mac-verify.sh
#
# It does not install anything outside the Go build cache and the build/
# directory. gomobile and gobind are tool dependencies in go.mod, so their
# version is pinned and `go tool` runs them; only Go module downloads need
# network access.
set -eu
cd "$(dirname "$0")/.."

say() { printf '== %s\n' "$*"; }
result() { printf 'RESULT %s=%s\n' "$1" "$2"; }

say "toolchain"
go version
xcodebuild -version | head -1

say "go checks"
fail=0
test -z "$(gofmt -l .)" && result gofmt ok || { gofmt -l .; result gofmt FAIL; fail=1; }
go vet ./... && result vet ok || { result vet FAIL; fail=1; }
go test -race -count=1 ./... && result test ok || { result test FAIL; fail=1; }
[ "$fail" -eq 0 ] || exit 1

say "gomobile"
# gomobile bind looks up gobind on PATH; build the pinned one into build/bin.
mkdir -p build/bin
go build -o build/bin/ golang.org/x/mobile/cmd/gomobile golang.org/x/mobile/cmd/gobind
export PATH="$PWD/build/bin:$PATH"
gomobile init

say "bind"
rm -rf build/VeneraCatCore.xcframework
start=$(date +%s)
# clang takes the macOS slice's deployment target from the SDK in use, so a build on
# macOS 27 makes objects newer than a client whose Package.swift says macOS 26, and every
# link against it warns. Pin the floor with gomobile's own flag: gomobile does **not**
# read MACOSX_DEPLOYMENT_TARGET (checked against its source and by experiment), so
# exporting that variable looks like a fix and changes nothing.
macos_version="${MACOS_VERSION:-26.0}"
if gomobile bind -trimpath -ldflags="-s -w" -target=ios,iossimulator,macos -iosversion=16 \
	-macosversion="$macos_version" -o build/VeneraCatCore.xcframework ./mobile; then
	result bind ok
else
	result bind FAIL
	exit 1
fi
result bind_seconds $(( $(date +%s) - start ))

say "sizes"
result xcframework_total "$(du -sh build/VeneraCatCore.xcframework | cut -f1)"
for slice in build/VeneraCatCore.xcframework/*/; do
	name=$(basename "$slice")
	bin=$(find "$slice" -type f -name VeneraCatCore -perm -u+x 2>/dev/null | head -1)
	[ -z "$bin" ] && bin=$(find "$slice" -type f -name VeneraCatCore | head -1)
	[ -n "$bin" ] || continue
	result "slice_$name" "$(du -h "$bin" | cut -f1) $(lipo -archs "$bin" 2>/dev/null)"
done
# Stripped device binary size is what the app download grows by (before App
# Store thinning and compression).
dev=$(find build/VeneraCatCore.xcframework/ios-arm64 -type f -name VeneraCatCore | head -1)
if [ -n "$dev" ]; then
	cp "$dev" build/device.bin && strip -x build/device.bin 2>/dev/null || true
	result device_binary_stripped "$(du -h build/device.bin | cut -f1)"
	result device_binary_gzip "$(gzip -9c build/device.bin | wc -c | awk '{printf "%.1fM", $1/1048576}')"
fi
say "done"
