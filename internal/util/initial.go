//go:build client

package util

import (
	"bufio"
	"easytun/internal/config"
	"easytun/internal/protocol"
	"easytun/internal/tun"
	"fmt"
	"log"
	"os"
	"runtime/debug"
)

func InitAll(localPath string) {
	defer func() {
		if err := recover(); err != nil {
			fmt.Fprintf(os.Stderr, "\n--- Panic ---\n")
			fmt.Fprintf(os.Stderr, "错误详情: %v\n\n", err)
			debug.PrintStack()

			fmt.Println("程序已崩溃。按 [回车键] 退出...")
			bufio.NewReader(os.Stdin).ReadString('\n')
			os.Exit(1)
		}
	}()
	config.InitConfig(localPath)
	protocol.InitChaCha()
	err := tun.InitWintunDLL()
	if err != nil {
		log.Fatal(err)
	}
}
