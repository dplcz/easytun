//go:build client

package util

import (
	"encoding/binary"
	"errors"
	"net"
)

// BuildDNSResponse 创建DNS响应
//func BuildDNSResponse(rawResp []byte, localIp, dnsServerIp net.IP) ([]byte, error) {
//	dstPort := binary.BigEndian.Uint16(rawResp[:2])
//	// 1. 构造 IP 层
//	ipLayer := &layers.IPv4{
//		Version:  4,
//		TTL:      64,
//		Protocol: layers.IPProtocolUDP,
//		SrcIP:    dnsServerIp,
//		DstIP:    localIp,
//		Flags:    layers.IPv4DontFragment,
//	}
//
//	// 2. 构造 UDP 层
//	udpLayer := &layers.UDP{
//		SrcPort: layers.UDPPort(53),
//		DstPort: layers.UDPPort(dstPort),
//	}
//	// 关键：关联网络层以自动计算 UDP 伪头部校验和
//	udpLayer.SetNetworkLayerForChecksum(ipLayer)
//
//	// 3. 序列化并自动填充长度和校验和
//	buf := gopacket.NewSerializeBuffer()
//	opts := gopacket.SerializeOptions{
//		ComputeChecksums: true, // 核心功能：自动计算所有校验和
//		FixLengths:       true, // 核心功能：自动修正所有 Header 长度
//	}
//
//	err := gopacket.SerializeLayers(buf, opts,
//		ipLayer,
//		udpLayer,
//		gopacket.Payload(rawResp[2:]),
//	)
//	if err != nil {
//		return nil, err
//	}
//
//	return buf.Bytes(), nil
//}

func BuildDNSResponse(rawResp []byte, localIp, dnsServerIp net.IP) ([]byte, error) {
	if len(rawResp) < 2 {
		return nil, errors.New("raw response too short")
	}

	// 1. 提取目标端口 (原始 DNS 请求中的源端口)
	dstPort := binary.BigEndian.Uint16(rawResp[:2])
	dnsPayload := rawResp[2:]

	// 2. 准备缓冲区
	// IPv4 Header (20 bytes) + UDP Header (8 bytes) + Payload
	totalLen := 20 + 8 + len(dnsPayload)
	packet := make([]byte, totalLen)

	// --- 3. 构造 IPv4 首部 ---
	packet[0] = 0x45 // Version 4, IHL 5 (20 bytes)
	packet[1] = 0x00 // DSCP/ECN
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen))
	binary.BigEndian.PutUint16(packet[4:6], 0)      // Identification
	binary.BigEndian.PutUint16(packet[6:8], 0x4000) // Flags: Don't Fragment
	packet[8] = 64                                  // TTL
	packet[9] = 17                                  // Protocol: UDP (17)
	copy(packet[12:16], dnsServerIp.To4())
	copy(packet[16:20], localIp.To4())
	// 计算 IP 校验和 (仅包含 IP 首部)
	binary.BigEndian.PutUint16(packet[10:12], checksum(packet[:20]))

	// --- 4. 构造 UDP 首部 ---
	udpOffset := 20
	binary.BigEndian.PutUint16(packet[udpOffset:udpOffset+2], 53)        // Src Port
	binary.BigEndian.PutUint16(packet[udpOffset+2:udpOffset+4], dstPort) // Dst Port
	binary.BigEndian.PutUint16(packet[udpOffset+4:udpOffset+6], uint16(8+len(dnsPayload)))
	binary.BigEndian.PutUint16(packet[udpOffset+6:udpOffset+8], 0) // 先置 0

	// --- 5. 复制 DNS Payload ---
	copy(packet[28:], dnsPayload)

	// --- 6. 计算 UDP 校验和 (包含伪首部) ---
	udpCheck := udpChecksum(packet[12:16], packet[16:20], packet[20:])
	binary.BigEndian.PutUint16(packet[udpOffset+6:udpOffset+8], udpCheck)

	return packet, nil
}

// checksum 计算标准 Internet Checksum (RFC 1071)
func checksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i < len(data)-1; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// udpChecksum 计算包含伪首部的 UDP 校验和
func udpChecksum(src, dst net.IP, udpData []byte) uint16 {
	var pseudoHeader []byte
	pseudoHeader = append(pseudoHeader, src.To4()...)
	pseudoHeader = append(pseudoHeader, dst.To4()...)
	pseudoHeader = append(pseudoHeader, 0)  // Zero byte
	pseudoHeader = append(pseudoHeader, 17) // Protocol UDP

	lengthBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lengthBuf, uint16(len(udpData)))
	pseudoHeader = append(pseudoHeader, lengthBuf...)

	// 计算 伪首部 + UDP数据 的校验和
	fullData := append(pseudoHeader, udpData...)
	return checksum(fullData)
}
