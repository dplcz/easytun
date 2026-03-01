package util

import (
	"encoding/binary"
	"net"
)

func IpToKey(ip net.IP) [4]byte {
	v4Ip := ip.To4()
	return [4]byte{
		v4Ip[0], v4Ip[1], v4Ip[2], v4Ip[3],
	}
}

func UDPAddrToBytes(addr *net.UDPAddr, natType uint8) []byte {
	buf := make([]byte, 7)
	copy(buf[0:4], addr.IP.To4())
	binary.BigEndian.PutUint16(buf[4:6], uint16(addr.Port))
	buf[6] = natType
	return buf
}

func BytesToIP(buf []byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   buf[0:4],
		Port: int(binary.BigEndian.Uint16(buf[4:6])),
	}
}
