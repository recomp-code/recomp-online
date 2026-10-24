#!/bin/bash
# build.sh: the voice engine's dependencies and the Windows DLL, from a Mac.
#
#   voice/include/opus/       Opus' headers
#   voice/lib/darwin-arm64/   libopus.a (the macOS launcher links it through cgo: voice_darwin.go)
#   voice/miniaudio.h         miniaudio (one header; public domain or MIT-0)
#   voice/OPUS-COPYING        Opus' license (BSD)
#   recompvoice_windows_amd64.dll   the engine for Windows (voice_windows.go embeds it: the launcher stays one .exe)
#
# All of them are committed; run this only to change a version. Opus and miniaudio are pinned by SHA-256. The
# Windows DLL is cross-compiled with zig (ZIG=path, default deps-mac/zig/zig next to the checkout).
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ZIG="${ZIG:-$HERE/../../../deps-mac/zig/zig}"
OPUS_VER=1.5.2
OPUS_SHA=65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1
MA_VER=0.11.22
MA_SHA=9019743287e443c55e5737a7297f38e5e358561701d6db2d905afb114390c410
W="$(mktemp -d)"
trap 'rm -rf "$W"' EXIT

fetch() { # url sha out
	curl -sfL -o "$3" "$1"
	echo "$2  $3" | shasum -a 256 -c - >/dev/null || { echo "checksum failed: $1" >&2; exit 1; }
}

fetch "https://downloads.xiph.org/releases/opus/opus-$OPUS_VER.tar.gz" $OPUS_SHA "$W/opus.tgz"
fetch "https://raw.githubusercontent.com/mackron/miniaudio/$MA_VER/miniaudio.h" $MA_SHA "$W/miniaudio.h"
tar xzf "$W/opus.tgz" -C "$W"
OPUS="$W/opus-$OPUS_VER"
cp "$W/miniaudio.h" "$HERE/miniaudio.h"
mkdir -p "$HERE/include/opus"
cp "$OPUS"/include/opus*.h "$HERE/include/opus/"
cp "$OPUS/COPYING" "$HERE/OPUS-COPYING" # Opus' BSD license: it goes with the binaries

opus_cmake() { # build-dir extra-args...
	local b="$1"; shift
	cmake -S "$OPUS" -B "$b" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DOPUS_BUILD_PROGRAMS=OFF \
		-DOPUS_BUILD_TESTING=OFF -DOPUS_INSTALL_PKG_CONFIG_MODULE=OFF -DOPUS_INSTALL_CMAKE_CONFIG_MODULE=OFF "$@" >/dev/null
	cmake --build "$b" -j 8 >/dev/null
}

# macOS (the app's floor is 14.0)
opus_cmake "$W/mac" -DCMAKE_OSX_ARCHITECTURES=arm64 -DCMAKE_OSX_DEPLOYMENT_TARGET=14.0
mkdir -p "$HERE/lib/darwin-arm64"
cp "$W/mac/libopus.a" "$HERE/lib/darwin-arm64/"

# Windows x64 through zig's clang and MinGW headers
cat >"$W/zcc" <<EOF
#!/bin/sh
exec "$ZIG" cc -target x86_64-windows-gnu "\$@"
EOF
cat >"$W/zar" <<EOF
#!/bin/sh
exec "$ZIG" ar "\$@"
EOF
cat >"$W/zranlib" <<EOF
#!/bin/sh
exec "$ZIG" ranlib "\$@"
EOF
chmod +x "$W/zcc" "$W/zar" "$W/zranlib"
opus_cmake "$W/win" -DCMAKE_SYSTEM_NAME=Windows -DCMAKE_SYSTEM_PROCESSOR=AMD64 -DCMAKE_C_COMPILER="$W/zcc" \
	-DCMAKE_AR="$W/zar" -DCMAKE_RANLIB="$W/zranlib"
"$W/zcc" -shared -O2 -fno-strict-aliasing -DNDEBUG -I"$HERE/include" -o "$HERE/../recompvoice_windows_amd64.dll" \
	"$HERE/engine.c" "$HERE/audio_ma.c" "$W/win/libopus.a" -lws2_32 -lole32 -lwinmm -s
rm -f "$HERE"/../*.lib "$HERE"/../*.pdb "$HERE"/../*.obj # zig's import library and objects: not needed
ls -la "$HERE/lib/darwin-arm64/libopus.a" "$HERE/../recompvoice_windows_amd64.dll"
