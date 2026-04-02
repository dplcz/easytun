package util

import (
	"encoding/binary"
	"net"
	"net/netip"
)

// IpToKey 将 net.IP 转换为固定长度的 [4]byte，用于 map 键
func IpToKey(ip net.IP) [4]byte {
	v4Ip := ip.To4()
	if v4Ip == nil {
		return [4]byte{}
	}
	return [4]byte{
		v4Ip[0], v4Ip[1], v4Ip[2], v4Ip[3],
	}
}

// UDPAddrToBytes 将 UDPAddr 转换为 7 字节 (4 字节 IP + 2 字节端口 + 1 字节 NAT 类型)
func UDPAddrToBytes(addr *net.UDPAddr, natType uint8) []byte {
	buf := make([]byte, 7)
	copy(buf[0:4], addr.IP.To4())
	binary.BigEndian.PutUint16(buf[4:6], uint16(addr.Port))
	buf[6] = natType
	return buf
}

// UDPAddrPortToBytes 将 netip.AddrPort 转换为 7 字节 (优化版)
func UDPAddrPortToBytes(addr netip.AddrPort, natType uint8) []byte {
	buf := make([]byte, 7)
	ip := addr.Addr().As4()
	copy(buf[0:4], ip[:])
	binary.BigEndian.PutUint16(buf[4:6], addr.Port())
	buf[6] = natType
	return buf
}

// BytesToIP 将 6 字节数据解析回 UDPAddr (用于客户端)
func BytesToIP(buf []byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IP(buf[0:4]),
		Port: int(binary.BigEndian.Uint16(buf[4:6])),
	}
}
