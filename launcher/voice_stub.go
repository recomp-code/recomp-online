//go:build !windows && !(darwin && arm64)

package main

// [voice] No voice engine built for this system (only macOS on Apple silicon and Windows x64 have one).

import "errors"

func rvLoad() error                                             { return errors.New("el chat de voz no está para este sistema") }
func rvStart(host string, port int, token string, slot int) int { return -1 }
func rvStop()                                                   {}
func rvSet(key, v int)                                          {}
func rvPeer(slot, vol, m int)                                   {}
func rvDevice(capture int, name string) int                     { return -1 }
func rvDevices(capture int) []string                            { return nil }
func rvStats(out []int32)                                       {}
