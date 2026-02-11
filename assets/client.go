//go:build client

package assets

import _ "embed"

//go:embed amd64/wintun.dll
var WintunAmd64 []byte

//go:embed example/config.json
var ConfigBytes []byte
