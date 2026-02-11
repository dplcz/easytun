//go:build server

package assets

import _ "embed"

//go:embed example/server_config.json
var ConfigBytes []byte
