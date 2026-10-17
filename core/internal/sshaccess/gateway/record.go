package gateway

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/grantra-dev/grantra/core/internal/sshaccess"
)

// recorder writes a session in asciicast v2 (https://docs.asciinema.org/manual/asciicast/v2/):
// a header line, then one JSON array per event. It also tracks the commands
// typed. The gateway takes what it holds with drain and stores it.
type recorder struct {
	mu        sync.Mutex
	start     time.Time
	now       func() time.Time
	buf       bytes.Buffer
	written   int64
	max       int64
	truncated bool
	carry     []byte
	lines     lineTracker
	commands  []sshaccess.Command
	// full is called once, when the recording reaches its limit.
	full func()
}

func newRecorder(now func() time.Time, max int64, width, height int, term, title string) *recorder {
	r := &recorder{start: now(), now: now, max: max}
	header := struct {
		Version   int               `json:"version"`
		Width     int               `json:"width"`
		Height    int               `json:"height"`
		Timestamp int64             `json:"timestamp"`
		Title     string            `json:"title"`
		Env       map[string]string `json:"env,omitempty"`
	}{2, width, height, r.start.Unix(), title, nil}
	if term != "" {
		header.Env = map[string]string{"TERM": term}
	}
	line, _ := json.Marshal(header)
	r.add(append(line, '\n'))
	return r
}

// add appends one line unless the recording is full: then it stops, so a
// recording is always the start of the session.
func (r *recorder) add(line []byte) {
	if r.truncated {
		return
	}
	if r.written+int64(len(line)) > r.max {
		r.truncated = true
		if r.full != nil {
			r.full()
		}
		return
	}
	r.buf.Write(line)
	r.written += int64(len(line))
}

// setFull sets what to do when the recording reaches its limit; f runs with
// the recorder locked, so it must not call back into it.
func (r *recorder) setFull(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.full = f
	if r.truncated {
		f()
	}
}

func (r *recorder) offset() float64 {
	// Microseconds are plenty for playback.
	return float64(r.now().Sub(r.start).Microseconds()) / 1e6
}

func (r *recorder) event(code, data string) {
	line, _ := json.Marshal([]any{r.offset(), code, data})
	r.add(append(line, '\n'))
}

// output records what the server sent. A character split across two reads
// waits for its second half, so the JSON keeps valid UTF-8.
func (r *recorder) output(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := append(r.carry, p...)
	cut := completeUTF8(data)
	r.carry = append([]byte(nil), data[cut:]...)
	if cut > 0 {
		r.event("o", string(data[:cut]))
		r.lines.output(r.now(), data[:cut])
		r.collect()
	}
}

// input tracks what the person typed, for the commands.
func (r *recorder) input(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines.input(r.now(), r.offset(), p)
	r.collect()
}

func (r *recorder) resize(width, height int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.event("r", strconv.Itoa(width)+"x"+strconv.Itoa(height))
}

// command records a command run without a shell (ssh target command).
func (r *recorder) command(cmd string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, sshaccess.Command{At: r.now(), Offset: r.offset(), Command: cmd})
}

func (r *recorder) collect() {
	r.commands = append(r.commands, r.lines.done...)
	r.lines.done = nil
}

// drain returns what was recorded since the last drain. At the end of the
// session it also gives up the commands still waiting for their echo.
func (r *recorder) drain(final bool) (data []byte, commands []sshaccess.Command, truncated bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if final {
		if len(r.carry) > 0 {
			r.event("o", strings.ToValidUTF8(string(r.carry), "�"))
			r.carry = nil
		}
		r.lines.flush(r.now(), true)
	} else {
		r.lines.flush(r.now(), false)
	}
	r.collect()
	data = append([]byte(nil), r.buf.Bytes()...)
	r.buf.Reset()
	commands, r.commands = r.commands, nil
	return data, commands, r.truncated
}

// completeUTF8 returns how much of p ends on a whole character: at most the
// last three bytes can be the start of one still arriving.
func completeUTF8(p []byte) int {
	for i := len(p) - 1; i >= 0 && i >= len(p)-3; i-- {
		if utf8.RuneStart(p[i]) {
			if !utf8.FullRune(p[i:]) {
				return i
			}
			break
		}
	}
	return len(p)
}

// lineTracker turns keystrokes into commands, best effort: the recording is
// the real record. In a terminal a line only counts when the server echoed
// it back by the end of that line, so what someone types at a password
// prompt (not echoed) is never indexed. Without a terminal nothing is
// echoed and every line counts.
type lineTracker struct {
	noEcho  bool
	line    []byte
	escape  int // 0 none, 1 after ESC, 2 inside a CSI or SS3 sequence
	started time.Time
	offset  float64
	// echo is what the server sent since the line's first key.
	echo    []byte
	pending []pendingLine
	done    []sshaccess.Command
}

type pendingLine struct {
	sshaccess.Command
	echo  []byte
	until time.Time
}

// echoWait is how long a typed line waits for its echo.
const echoWait = 5 * time.Second

// maxEcho bounds the output kept while a line is typed.
const maxEcho = 16 << 10

func (t *lineTracker) input(now time.Time, offset float64, p []byte) {
	for _, b := range p {
		switch t.escape {
		case 1:
			if b == '[' || b == 'O' {
				t.escape = 2
			} else {
				t.escape = 0
			}
			continue
		case 2:
			if b >= 0x40 && b <= 0x7e {
				t.escape = 0
			}
			continue
		}
		switch {
		case b == 0x1b:
			t.escape = 1
		case b == '\r' || b == '\n':
			t.enter(now)
		case b == 0x7f || b == 0x08:
			if len(t.line) > 0 {
				_, size := utf8.DecodeLastRune(t.line)
				t.line = t.line[:len(t.line)-size]
			}
		case b == 0x15 || b == 0x03: // Ctrl-U, Ctrl-C
			t.line = t.line[:0]
		case b == 0x17: // Ctrl-W
			trimmed := bytes.TrimRight(t.line, " ")
			t.line = trimmed[:bytes.LastIndexByte(trimmed, ' ')+1]
		case b < 0x20:
		default:
			if len(t.line) == 0 {
				t.started, t.offset = now, offset
				t.echo = t.echo[:0]
			}
			if len(t.line) < 4096 {
				t.line = append(t.line, b)
			}
		}
	}
}

func (t *lineTracker) enter(now time.Time) {
	cmd := strings.TrimSpace(strings.ToValidUTF8(string(t.line), ""))
	t.line = t.line[:0]
	echo := append([]byte(nil), t.echo...)
	t.echo = t.echo[:0]
	if cmd == "" {
		return
	}
	c := sshaccess.Command{At: t.started, Offset: t.offset, Command: cmd}
	if t.noEcho {
		t.done = append(t.done, c)
		return
	}
	l := pendingLine{Command: c, echo: echo, until: now.Add(echoWait)}
	if echoed(l) {
		t.done = append(t.done, c)
		return
	}
	t.pending = append(t.pending, l)
}

func echoed(l pendingLine) bool {
	return bytes.Contains(stripControls(l.echo), []byte(l.Command.Command))
}

// output feeds what the server sent to the line being typed and to the
// lines whose echo has not come back yet. A waiting line only takes output
// up to the end of its own line: what follows is the command's output,
// which must not count as its echo.
func (t *lineTracker) output(now time.Time, p []byte) {
	if len(t.line) > 0 && len(t.echo) < maxEcho {
		t.echo = append(t.echo, p[:min(len(p), maxEcho-len(t.echo))]...)
	}
	kept := t.pending[:0]
	for _, l := range t.pending {
		end := bytes.IndexAny(p, "\r\n")
		if end < 0 {
			end = len(p)
		}
		if len(l.echo) < maxEcho {
			l.echo = append(l.echo, p[:min(end, maxEcho-len(l.echo))]...)
		}
		switch {
		case echoed(l):
			t.done = append(t.done, l.Command)
		case end == len(p) && now.Before(l.until):
			kept = append(kept, l)
		}
	}
	t.pending = kept
}

// flush drops the lines whose echo never came, or all of them at the end.
func (t *lineTracker) flush(now time.Time, all bool) {
	kept := t.pending[:0]
	for _, l := range t.pending {
		if !all && now.Before(l.until) {
			kept = append(kept, l)
		}
	}
	t.pending = kept
}

// stripControls removes terminal escape sequences and control characters,
// and applies backspaces, from what a server echoed.
func stripControls(p []byte) []byte {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch {
		case b == 0x1b:
			// Skip CSI (ESC [ ... final) and other two-byte sequences.
			if i+1 < len(p) && (p[i+1] == '[' || p[i+1] == 'O' || p[i+1] == ']') {
				osc := p[i+1] == ']'
				i += 2
				for ; i < len(p); i++ {
					if osc && (p[i] == 0x07 || (p[i] == 0x1b && i+1 < len(p) && p[i+1] == '\\')) {
						break
					}
					if !osc && p[i] >= 0x40 && p[i] <= 0x7e {
						break
					}
				}
			} else {
				i++
			}
		case b == 0x08:
			if len(out) > 0 {
				_, size := utf8.DecodeLastRune(out)
				out = out[:len(out)-size]
			}
		case b < 0x20 && b != '\t':
			out = append(out, '\n')
		default:
			out = append(out, b)
		}
	}
	return out
}
