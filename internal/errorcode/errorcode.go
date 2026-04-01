package errorcode

import (
	"errors"
)

var (
	// InvalidMagic 非法魔数
	InvalidMagic = errors.New("invalid magic")
	// PayloadMismatch 数据长度不匹配
	PayloadMismatch = errors.New("payload length mismatch")
	// PacketTooShort 包长度过短
	PacketTooShort = errors.New("packet too short")
	// MissingPublicKey 缺少公钥
	MissingPublicKey = errors.New("missing public key")
)
