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
// Only the opcode is copied out of it: the other elements, such as a
// screen's image data, are read from raw when asked for.
type instruction struct {
	op  string
	raw []byte
	// bounds are where each element starts and ends in raw, in pairs.
	bounds []int
}

func (i instruction) opcode() string { return i.op }

// args are the elements after the opcode.
func (i instruction) args() []string { return i.elemsFrom(2) }

// elems are all the elements, opcode first.
func (i instruction) elems() []string { return i.elemsFrom(0) }

func (i instruction) elemsFrom(k int) []string {
	out := make([]string, 0, (len(i.bounds)-k)/2)
	for ; k < len(i.bounds); k += 2 {
		out = append(out, string(i.raw[i.bounds[k]:i.bounds[k+1]]))
	}
	return out
}

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
	var bounds []int
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
		if err := g.readChars(n); err != nil {
			return instruction{}, err
		}
		bounds = append(bounds, start, g.buf.Len())
		c, err := g.r.ReadByte()
		if err != nil {
			return instruction{}, eofInside(err, true)
		}
		g.buf.WriteByte(c)
		switch {
		case c == ';':
			raw := bytes.Clone(g.buf.Bytes())
			return instruction{op: string(raw[bounds[0]:bounds[1]]), raw: raw, bounds: bounds}, nil
		case c != ',':
			return instruction{}, errGuacSyntax
		case len(bounds) == 2*guacMaxElements:
			return instruction{}, errors.New("a Guacamole instruction has too many elements")
		}
	}
}

// readChars copies the next n characters to buf. Runs of ASCII, most of
// what guacd sends (image data is base64), go in one piece; anything else
// one character at a time, checked to be UTF-8.
func (g *guacReader) readChars(n int) error {
	for n > 0 {
		if _, err := g.r.Peek(1); err != nil {
			return eofInside(err, true)
		}
		p, _ := g.r.Peek(min(n, g.r.Buffered()))
		ascii := 0
		for ascii < len(p) && p[ascii] < utf8.RuneSelf {
			ascii++
		}
		if ascii > 0 {
			g.buf.Write(p[:ascii])
			g.r.Discard(ascii) //nolint:errcheck // what Peek returned is buffered
			n -= ascii
			continue
		}
		r, size, err := g.r.ReadRune()
		if err != nil {
			return eofInside(err, true)
		}
		if r == utf8.RuneError && size == 1 {
			return errGuacSyntax
		}
		g.buf.WriteRune(r)
		n--
	}
	return nil
}

// eofInside turns an end of input in the middle of an instruction into an
// error of its own: only between instructions is it a clean end.
func eofInside(err error, inside bool) error {
	if inside && errors.Is(err, io.EOF) {
		return errors.New("the Guacamole stream ended in the middle of an instruction")
	}
	return err
}
