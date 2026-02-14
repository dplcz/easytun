package transport

import (
	"context"
	"easytun/internal/config"
	"testing"
)

func TestServer(t *testing.T) {
	config.InitConfig()
	hub := NewHub()
	hub.Run(context.Background())
}
