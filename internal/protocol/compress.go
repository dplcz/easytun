package protocol

import (
	"fmt"
	"sync"

	"github.com/pierrec/lz4/v4"
)

var probeLen = lz4.CompressBlockBound(64)

// Compress 尝试压缩数据。
// 仅对大于 128 字节的数据进行压缩。
func (g *GamePacket) compress(pool *sync.Pool) bool {
	payloadLen := int(g.Length - 16)

	if payloadLen <= 128 {
		return false
	}
	// 1. 尝试对前 64 字节进行探测
	probeBuf := pool.Get().([]byte)
	if cap(probeBuf) < probeLen {
		pool.Put(probeBuf[:0])
		probeBuf = make([]byte, probeLen)
	}
	defer pool.Put(probeBuf[:0])

	n, err := lz4.CompressBlock(g.Payload[:64], probeBuf, nil)
	if err != nil || n > 50 {
		return false
	}
	// 2. 正式执行全量压缩
	dst := pool.Get().([]byte)
	bound := lz4.CompressBlockBound(payloadLen)
	if cap(dst) < bound {
		pool.Put(dst[:0])
		dst = make([]byte, bound)
	}
	dst = dst[:bound]

	n, err = lz4.CompressBlock(g.Payload, dst, nil)
	if err != nil || n >= payloadLen || n == 0 {
		pool.Put(dst[:0])
		return false
	}
	pool.Put(g.Payload[:0])
	g.Payload = dst[:n]
	g.Length = uint16(n + 16)
	return true
}

// Decompress 解压数据
func (g *GamePacket) decompress(pool *sync.Pool) error {
	dst := pool.Get().([]byte)
	if cap(dst) < 2048 {
		pool.Put(dst[:0])
		dst = make([]byte, 2048)
	}
	dst = dst[:cap(dst)]
	defer pool.Put(dst[:0])

	n, err := lz4.UncompressBlock(g.Payload, dst)
	if err != nil {
		return fmt.Errorf("LZ4 解压失败: %w", err)
	}
	reqLen := n + HeaderLength
	if reqLen > cap(g.RawData) {
		tempBuf := make([]byte, reqLen)
		copy(tempBuf[:HeaderLength], g.RawData[:HeaderLength])
		copy(tempBuf[HeaderLength:], dst[:n])
		pool.Put(g.RawData[:0])
		g.RawData = tempBuf
		g.Payload = tempBuf[HeaderLength:]
	} else {
		g.RawData = g.RawData[:reqLen]
		copy(g.RawData[HeaderLength:], dst[:n])
		g.Payload = g.RawData[HeaderLength:]
	}

	return nil
}
