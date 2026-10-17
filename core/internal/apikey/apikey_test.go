package apikey

import (
	"bytes"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	a, shown, err := New("grantra_x_")
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := New("grantra_x_")
	if !strings.HasPrefix(a, "grantra_x_") || len(a) != len("grantra_x_")+43 || a == b {
		t.Errorf("secrets %q %q", a, b)
	}
	if shown != a[:len("grantra_x_")+6] {
		t.Errorf("shown = %q", shown)
	}
	if bytes.Equal(Hash(a), Hash(b)) || !bytes.Equal(Hash(a), Hash(a)) || len(Hash(a)) != 32 {
		t.Error("hashes")
	}
}
