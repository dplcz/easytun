package stun

import (
	"fmt"

	s "github.com/dplcz/go-test-nat/stun"
)

const (
	TypeBlock = iota + 1
	TypeUnknown
	TypeCone
	TypeSymmetric
)

func GetNatType() (uint8, error) {
	natType, _, _, err := s.GetIpInfo()
	if err != nil {
		return 0, err
	}
	switch natType {
	case s.Blocked:
		return TypeBlock, nil
	case s.FullCone, s.RestrictNAT, s.RestrictPortNAT:
		return TypeCone, nil
	case s.SymmetricNAT:
		return TypeSymmetric, nil
	default:
		return 0, fmt.Errorf("unknown natType %d", natType)
	}
}
