//go:build server

package assets

import (
	_ "embed"
	"os"
)

//go:embed config/server_config.toml
var ConfigBytes []byte

//go:embed config/server_config.toml.example
var ConfigExampleBytes []byte

func init() {
	// 尝试释放 example 文件（如果不存在）
	if _, err := os.Stat("config.toml.example"); os.IsNotExist(err) {
		_ = os.WriteFile("config.toml.example", ConfigExampleBytes, 0644)
	}
}
