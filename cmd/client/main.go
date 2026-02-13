package main

import (
	"context"
	"flag"
	"game_tun/internal/transport"
	"game_tun/internal/util"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	isTest := flag.Bool("t", false, "是否启动测试模式 (默认 false)")
	interval := flag.Int("i", 5, "广播发送间隔秒数 (默认 0，不自动发送)")
	flag.Parse()
	second := time.Duration(*interval)

	util.InitAll()
	tp := transport.NewTransport()
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	// 监听 Ctrl+C (SIGINT) 和 强制退出 (SIGTERM)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		_ = <-sigChan
		log.Println("正在关闭...")
		cancel() // 触发 Context 取消，通知所有关联的协程停止工作
	}()
	if *isTest {
		tp.ListenAndServe(ctx, cancel, *isTest, &second)
	} else {
		tp.ListenAndServe(ctx, cancel, *isTest, nil)
	}

}
