package transport

import (
	"context"
	"testing"
)

func TestServer(t *testing.T) {
	hub := newHub()
	hub.Run(context.Background())
}
