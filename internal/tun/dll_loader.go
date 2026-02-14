//go:build client

package tun

import (
	"easytun/assets"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func InitWintunDLL() error {
	var dllBytes []byte
	var dllName = "wintun.dll"

	// 1. 根据系统架构选择正确的 DLL 内容
	switch runtime.GOARCH {
	case "amd64":
		dllBytes = assets.WintunAmd64
	case "386":
		return fmt.Errorf("不支持的架构: 386")
	case "arm64":
		// 如果你有 arm64 的 wintun.dll，在这里添加
		return fmt.Errorf("不支持的架构: arm64")
	default:
		return fmt.Errorf("不支持的架构: %s", runtime.GOARCH)
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exePath)
	dllPath := filepath.Join(dir, dllName)

	if _, err := os.Stat(dllPath); err == nil {
		// TODO 添加hash校验
		return nil
	}

	err = os.WriteFile(dllPath, dllBytes, 0755)
	if err != nil {
		return fmt.Errorf("无法释放 wintun.dll: %v", err)
	}

	return nil
}
