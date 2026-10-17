package proxy

import (
	"bufio"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func readAll(t *testing.T, s string) ([]instruction, error) {
	t.Helper()
	r := newGuacReader(bufio.NewReader(strings.NewReader(s)))
	var out []instruction
	for {
		in, err := r.read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, in)
	}
}

func TestGuacProtocol(t *testing.T) {
	// Lengths count characters, not bytes.
	enc := string(guacEncode("clipboard", "", "ñü€😀", ""))
	if enc != "9.clipboard,0.,4.ñü€😀,0.;" {
		t.Errorf("encode = %q", enc)
	}
	ins, err := readAll(t, enc+"4.sync,3.100;0.,4.ping,13.1700000000000;")
	if err != nil || len(ins) != 3 {
		t.Fatalf("read = %v %v", ins, err)
	}
	if ins[0].opcode() != "clipboard" || !slices.Equal(ins[0].args(), []string{"", "ñü€😀", ""}) || string(ins[0].raw) != enc {
		t.Errorf("first = %+v %q", ins[0].elems(), ins[0].raw)
	}
	if ins[2].opcode() != "" || ins[2].args()[0] != "ping" {
		t.Errorf("internal = %+v", ins[2].elems())
	}
	// A comma or semicolon inside an element is text.
	if ins, err := readAll(t, "3.key,3.a;b,1.1;"); err != nil || ins[0].args()[0] != "a;b" {
		t.Errorf("text with ; = %v %v", ins, err)
	}

	for _, bad := range []string{
		"4.sync,3.100",     // cut short
		"4.sync,3.10;",     // length does not match
		"x.sync;",          // not a length
		".sync;",           // no length
		"4.sync:3.100;",    // bad separator
		"1234567.x;",       // too many digits
		"99999.x;",         // too long
		"4.sync,3.1\xff0;", // not UTF-8
		strings.Repeat("1.a,", guacMaxElements) + "1.a;",
	} {
		if _, err := readAll(t, bad); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

// An element longer than the reader's buffer, with characters of every
// width falling on its edges, reads whole.
func TestGuacLongElement(t *testing.T) {
	text := strings.Repeat("iVBORw0KGgo=", 900) + "ñ€😀" + strings.Repeat("a😀", 300)
	enc := guacEncode("img", "1", text)
	r := newGuacReader(bufio.NewReaderSize(strings.NewReader(string(enc)+"4.sync,1.5;"), 16))
	in, err := r.read()
	if err != nil {
		t.Fatal(err)
	}
	if in.opcode() != "img" || !slices.Equal(in.args(), []string{"1", text}) || string(in.raw) != string(enc) {
		t.Errorf("long element read as %d bytes", len(in.raw))
	}
	if in, err := r.read(); err != nil || in.opcode() != "sync" || in.args()[0] != "5" {
		t.Errorf("next = %v %v", in.elems(), err)
	}
}

func BenchmarkGuacRead(b *testing.B) {
	enc := string(guacEncode("img", "1", "14", "0", "image/png", "0", "0")) +
		string(guacEncode("blob", "1", strings.Repeat("iVBORw0KGgoAAAANSUhEUgAA", 2000)))
	b.SetBytes(int64(len(enc)))
	for b.Loop() {
		r := newGuacReader(bufio.NewReader(strings.NewReader(enc)))
		for {
			if _, err := r.read(); err != nil {
				break
			}
		}
	}
}

func TestRDPSettings(t *testing.T) {
	for in, want := range map[string][2]string{
		`CORP\ana`: {"CORP", "ana"}, "ana@corp.example": {"", "ana@corp.example"}, "ana": {"", "ana"}, `\ana`: {"", `\ana`},
	} {
		if d, u := splitDomain(in); d != want[0] || u != want[1] {
			t.Errorf("%s = %s %s", in, d, u)
		}
	}
}
