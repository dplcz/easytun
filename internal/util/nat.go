package util

import (
	"easytun/internal/stun"
	"fmt"

	s "github.com/dplcz/go-test-nat/stun"
)

func GetNatType() (uint8, error) {
	natType, _, _, err := s.GetIpInfo()
	if err != nil {
		return 0, err
	}
	switch natType {
	case s.Blocked:
		return stun.TypeBlock, nil
	case s.FullCone, s.RestrictNAT, s.RestrictPortNAT:
		return stun.TypeCone, nil
	case s.SymmetricNAT:
		return stun.TypeSymmetric, nil
	default:
		return 0, fmt.Errorf("unknown natType %d", natType)
	}
}
