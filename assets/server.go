//go:build server

package assets

import _ "embed"

//go:embed config/server_config.json
var ConfigBytes []byte
