package util

import (
	"fmt"
	"net"
	"testing"
)

func TestTransform(t *testing.T) {
	ip := net.IPv4(10, 0, 6, 222)
	fmt.Println(ip)
	fmt.Println(IpToKey(ip))
}
