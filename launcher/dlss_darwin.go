package main

// No DLSS on macOS (the GS host draws with Metal; MetalFX is one of the quality presets).

func (g *game) dlssInfo() DLSSInfo { return DLSSInfo{} }

// addDLSS: files dropped on the window: nothing to add here.
func (g *game) addDLSS(paths []string) (int, []string) { return 0, nil }
