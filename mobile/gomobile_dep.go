//go:build gomobiledep

// This file exists only to keep golang.org/x/mobile in go.mod so that
// `gomobile bind` can find it and `go mod tidy` cannot drop it. It is never
// compiled into any real build: no target ever sets the gomobiledep tag.
//
// The version is PINNED. Do NOT run `go get -tool golang.org/x/mobile/cmd/gobind`
// as gomobile's own error message suggests: the current x/mobile declares
// `go 1.26.0` in its go.mod, which forces the main module's go directive from
// 1.23 to 1.26.0, which turns on Go 1.26's stricter vet printf analyzer, which
// fails upstream's own test build at
// internal/core/launch_monitor_test.go:490 ("format %q has arg LeftHanded of
// wrong type ...HandednessType"). The pin below declares `go 1.23.0` and keeps
// `go test ./...` green.
package mobile

import _ "golang.org/x/mobile/bind"
