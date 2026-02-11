//go:build client

package tun

import "context"

type Device interface {
	//// Read 返回一个干净的 IP 数据包 (已经过滤过垃圾包的)
	//Read() ([]byte, error)
	//
	//// Write 接收一个 IP 数据包并写入内核
	//Write([]byte) error

	// Name 返回网卡名
	Name() string

	LUID() uint64

	Close() error
	Start(ctx context.Context)
}
