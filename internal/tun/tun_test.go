package tun

import (
	"context"
	"testing"
)

func TestTun(t *testing.T) {
	tun := NewTun("temp", "10.0.1.1")
	ctx, _ := context.WithCancel(context.Background())
	tun.Start(ctx)

}
