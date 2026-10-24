// [voice] The voice engine and its backends, built into the macOS launcher (voice_darwin_arm64.go). cgo compiles the
// package's C as Objective-C (app_darwin.go's flags); the engine is plain C and does not mind.
#pragma clang diagnostic ignored "-Weverything"
#include "voice/engine.c"
#include "voice/audio_ma.c"
#include "voice/audio_vpio.c"
