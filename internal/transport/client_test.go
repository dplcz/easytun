package transport

import (
	"testing"
)

func TestClient(t *testing.T) {
	tp := NewTransport()

	tp.ListenAndServe()
}
