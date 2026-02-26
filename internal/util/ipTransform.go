package util

import "net"

func IpToKey(ip net.IP) [4]byte {
	v4Ip := ip.To4()
	return [4]byte{
		v4Ip[0], v4Ip[1], v4Ip[2], v4Ip[3],
	}
}
