//go:build android

// Recomp launcher on Android: the same Go code and the same frontend as on Windows and macOS, without Wails. The app
// (android/ in dbz-bt4-recomp) shows the frontend in a WebView; its JavaScript bridge sends every window.go.main.App
// call here as JSON (goCall) and this side sends the events back (uiEmit -> rbEmit). Built as a shared library:
//
//	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=<NDK>/aarch64-linux-android31-clang \
//	  go build -buildmode=c-shared -trimpath -ldflags "-s -w -X main.defaultServer=..." -o liblauncher.so
package main

/*
#include <stdlib.h>
void rbEmit(const char *name, const char *json);
char *rbPlatform(const char *op, const char *json);
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"
)

func main() {}

var (
	theApp  *App
	appOnce sync.Once
)

//export goInit
func goInit(root, server *C.char) {
	appOnce.Do(func() {
		if s := C.GoString(server); s != "" {
			defaultServer = s
		}
		androidRoot = C.GoString(root)
		theApp = NewApp(androidRoot, false)
		theApp.startup(context.Background())
	})
}

var errType = reflect.TypeOf((*error)(nil)).Elem()

// goCall runs App.<method>(args...) like Wails' binding: args is a JSON array, the answer {"r": result} or {"e": error}.
//
//export goCall
func goCall(method, args *C.char) *C.char {
	out := map[string]any{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				out = map[string]any{"e": fmt.Sprint(r)}
			}
		}()
		if theApp == nil {
			out["e"] = "el launcher no ha iniciado"
			return
		}
		m := reflect.ValueOf(theApp).MethodByName(C.GoString(method))
		if !m.IsValid() {
			out["e"] = "no existe " + C.GoString(method)
			return
		}
		var raw []json.RawMessage
		if err := json.Unmarshal([]byte(C.GoString(args)), &raw); err != nil {
			out["e"] = err.Error()
			return
		}
		t := m.Type()
		in := make([]reflect.Value, t.NumIn())
		for i := range in {
			v := reflect.New(t.In(i))
			if i < len(raw) {
				if err := json.Unmarshal(raw[i], v.Interface()); err != nil {
					out["e"] = err.Error()
					return
				}
			}
			in[i] = v.Elem()
		}
		res := m.Call(in)
		for i, r := range res {
			if t.Out(i).Implements(errType) {
				if !r.IsNil() {
					out["e"] = r.Interface().(error).Error()
				}
				continue
			}
			out["r"] = r.Interface()
		}
	}()
	return C.CString(asciiJSON(out))
}

// asciiJSON: JSON with everything past ASCII as \u escapes (UTF-16 pairs past the BMP), safe for JNI's NewStringUTF.
func asciiJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"e": err.Error()})
	}
	var sb strings.Builder
	for _, r := range string(b) {
		if r < 0x80 {
			sb.WriteRune(r)
			continue
		}
		for _, u := range utf16.Encode([]rune{r}) {
			fmt.Fprintf(&sb, `\u%04x`, u)
		}
	}
	return sb.String()
}

// platform asks the app (RecompBridge.platform in Java) for an Android service.
func platform(op string, payload any) (string, error) {
	cop, cj := C.CString(op), C.CString(asciiJSON(payload))
	defer C.free(unsafe.Pointer(cop))
	defer C.free(unsafe.Pointer(cj))
	r := C.rbPlatform(cop, cj)
	if r == nil {
		return "", errors.New("Android no respondió a " + op)
	}
	defer C.free(unsafe.Pointer(r))
	s := C.GoString(r)
	if e, ok := strings.CutPrefix(s, "error:"); ok {
		return "", errors.New(e)
	}
	return s, nil
}

func emitAndroid(name string, data any) {
	cn, cj := C.CString(name), C.CString(asciiJSON(data))
	C.rbEmit(cn, cj)
	C.free(unsafe.Pointer(cn))
	C.free(unsafe.Pointer(cj))
}
