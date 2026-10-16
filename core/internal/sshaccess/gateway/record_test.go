package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func commands(r *recorder, final bool) []string {
	_, cmds, _ := r.drain(final)
	out := []string{}
	for _, c := range cmds {
		out = append(out, c.Command)
	}
	return out
}

func TestCommandsNeedTheirEcho(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	r := newRecorder(clock, 1<<20, 80, 24, "xterm", "t")

	// Typed key by key, echoed key by key.
	for _, k := range "ls -la" {
		r.input([]byte(string(k)))
		r.output([]byte(string(k)))
	}
	r.input([]byte("\r"))
	r.output([]byte("\r\ntotal 0\r\n$ "))

	// Pasted with Enter in one go: the echo comes after.
	r.input([]byte("echo hi\r"))
	r.output([]byte("echo hi\r\nhi\r\n$ "))

	// A password: nothing echoed; output that happens to contain it after
	// the line ends does not count.
	r.input([]byte("sudo -i\r"))
	r.output([]byte("sudo -i\r\n[sudo] password for ana: "))
	r.input([]byte("hunter2\r"))
	r.output([]byte("\r\nhunter2 is not a command\r\n# "))

	// Editing: backspace, Ctrl-U, Ctrl-W, arrows.
	r.input([]byte("lss\x7f"))
	r.output([]byte("lss\b \b"))
	r.input([]byte("\r"))
	r.output([]byte("\r\n"))
	r.input([]byte("rm -rf /\x15"))
	r.output([]byte("rm -rf /\r\x1b[K"))
	r.input([]byte("cat a b\x17c\x1b[D\r"))
	r.output([]byte("cat a c\r\n"))

	got := commands(r, false)
	want := []string{"ls -la", "echo hi", "sudo -i", "ls", "cat a c"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %q, want %q", got, want)
	}

	// A line still waiting for its echo is dropped after a while, and at the
	// end.
	r.input([]byte("secret\r"))
	now = now.Add(10 * time.Second)
	if got := commands(r, false); len(got) != 0 {
		t.Errorf("unechoed line indexed: %q", got)
	}
	r.input([]byte("pending\r"))
	if got := commands(r, true); len(got) != 0 {
		t.Errorf("unechoed line indexed at the end: %q", got)
	}
}

func TestCommandsWithoutTerminal(t *testing.T) {
	r := newRecorder(time.Now, 1<<20, 80, 24, "", "t")
	r.lines.noEcho = true
	r.input([]byte("uptime\nwhoami\n"))
	if got := commands(r, false); strings.Join(got, "|") != "uptime|whoami" {
		t.Errorf("commands = %q", got)
	}
}

func TestRecordingIsAsciicast(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newRecorder(func() time.Time { return now }, 1<<20, 100, 30, "xterm-256color", "web-1 as deploy")
	now = now.Add(1500 * time.Millisecond)
	// "ñ" split across two reads stays whole.
	r.output([]byte("a\xc3"))
	r.output([]byte("\xb1b"))
	r.resize(120, 40)
	data, _, truncated := r.drain(true)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 || truncated {
		t.Fatalf("recording = %q", data)
	}
	var header struct {
		Version, Width, Height int
		Timestamp              int64
		Env                    map[string]string
	}
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil || header.Version != 2 || header.Width != 100 ||
		header.Timestamp != 1000 || header.Env["TERM"] != "xterm-256color" {
		t.Errorf("header = %s (%v)", lines[0], err)
	}
	if lines[1] != `[1.5,"o","a"]` || lines[2] != `[1.5,"o","ñb"]` || lines[3] != `[1.5,"r","120x40"]` {
		t.Errorf("events = %q", lines[1:])
	}

	// Past the cap the recording stops, and says so.
	small := newRecorder(time.Now, 200, 80, 24, "", "t")
	small.output([]byte(strings.Repeat("x", 500)))
	small.output([]byte("y"))
	data, _, truncated = small.drain(true)
	if !truncated || len(data) > 200 || strings.Contains(string(data), `"y"`) {
		t.Errorf("capped recording = %q, truncated %v", data, truncated)
	}
}
