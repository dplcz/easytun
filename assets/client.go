//go:build client

package assets

import (
	_ "embed"
	"os"
)

//go:embed amd64/wintun.dll
var WintunAmd64 []byte

//go:embed config/config.toml
var ConfigBytes []byte

//go:embed config/config.toml.example
var ConfigExampleBytes []byte

func init() {
	// 尝试释放 example 文件（如果不存在）
	if _, err := os.Stat("config.toml.example"); os.IsNotExist(err) {
		_ = os.WriteFile("config.toml.example", ConfigExampleBytes, 0644)
	}
}
