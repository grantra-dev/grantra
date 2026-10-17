package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// The Guacamole protocol, which guacd and the browser's Guacamole client
// speak: instructions like 4.size,1.0,4.1024,3.768; — each element is its
// length in characters (Unicode code points), a dot and the text; commas
// separate them and a semicolon ends the instruction. The first element is
// the opcode.

// instruction is one Guacamole instruction, with the text it came as.
type instruction struct {
	elems []string
	raw   []byte
}

func (i instruction) opcode() string { return i.elems[0] }

func (i instruction) args() []string { return i.elems[1:] }

// guacEncode writes an instruction.
func guacEncode(elems ...string) []byte {
	var b bytes.Buffer
	for i, e := range elems {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(utf8.RuneCountInString(e)))
		b.WriteByte('.')
		b.WriteString(e)
	}
	b.WriteByte(';')
	return b.Bytes()
}

// Limits on what is read: guacd's own are 8,192 characters per element and
// 128 elements, but its args instruction lists every parameter.
const (
	guacMaxElement  = 64 << 10
	guacMaxElements = 512
)

var errGuacSyntax = errors.New("not a valid Guacamole instruction")

// guacReader reads instructions from r.
type guacReader struct {
	r   *bufio.Reader
	buf bytes.Buffer
}

func newGuacReader(r *bufio.Reader) *guacReader { return &guacReader{r: r} }

// read returns the next instruction.
func (g *guacReader) read() (instruction, error) {
	g.buf.Reset()
	var elems []string
	for {
		// The length, at most 6 digits.
		n, digits := 0, 0
		for {
			c, err := g.r.ReadByte()
			if err != nil {
				return instruction{}, eofInside(err, g.buf.Len()+digits > 0)
			}
			g.buf.WriteByte(c)
			if c == '.' && digits > 0 {
				break
			}
			if c < '0' || c > '9' || digits == 6 {
				return instruction{}, errGuacSyntax
			}
			n = n*10 + int(c-'0')
			digits++
		}
		if n > guacMaxElement {
			return instruction{}, fmt.Errorf("a Guacamole element of %d characters is too long", n)
		}
		start := g.buf.Len()
		for range n {
			r, size, err := g.r.ReadRune()
			if err != nil {
				return instruction{}, eofInside(err, true)
			}
			if r == utf8.RuneError && size == 1 {
				return instruction{}, errGuacSyntax
			}
			g.buf.WriteRune(r)
		}
		elems = append(elems, string(g.buf.Bytes()[start:]))
		c, err := g.r.ReadByte()
		if err != nil {
			return instruction{}, eofInside(err, true)
		}
		g.buf.WriteByte(c)
		switch {
		case c == ';':
			return instruction{elems: elems, raw: bytes.Clone(g.buf.Bytes())}, nil
		case c != ',':
			return instruction{}, errGuacSyntax
		case len(elems) == guacMaxElements:
			return instruction{}, errors.New("a Guacamole instruction has too many elements")
		}
	}
}

// eofInside turns an end of input in the middle of an instruction into an
// error of its own: only between instructions is it a clean end.
func eofInside(err error, inside bool) error {
	if inside && errors.Is(err, io.EOF) {
		return errors.New("the Guacamole stream ended in the middle of an instruction")
	}
	return err
}
