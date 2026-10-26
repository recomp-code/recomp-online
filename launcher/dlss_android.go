//go:build android

package main

// No DLSS on Android (no NVIDIA GPU): the native renderer upscales with FSR 2.
func (g *game) dlssInfo() DLSSInfo { return DLSSInfo{} }

func (g *game) addDLSS(paths []string) (int, []string) { return 0, nil }
