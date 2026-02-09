package protocol

import (
	"fmt"
	"testing"
)

func TestGamePacket_Encode_Decode(t *testing.T) {

	gp := NewGamePacket(3, 2, 1, []byte("abcdefg"))
	encodedData, err := gp.Encode()
	if err != nil {
		t.Error(err.Error())
	}
	fmt.Println(encodedData)
	err = gp.Decode(encodedData)
	if err != nil {
		t.Error(err.Error())
	}
	fmt.Println(gp.Encode())

}
