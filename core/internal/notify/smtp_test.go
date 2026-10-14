package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTP accepts one message on a local port and returns what it got.
func fakeSMTP(t *testing.T, offerStartTLS bool) (port int, got <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 fake ESMTP")
		var log strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				out <- log.String()
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				if offerStartTLS {
					say("250-fake\r\n250 STARTTLS")
				} else {
					say("250 fake")
				}
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				log.WriteString(strings.TrimSpace(line) + "\n")
				say("250 ok")
			case cmd == "DATA":
				say("354 go on")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					log.WriteString(l)
				}
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				out <- log.String()
				return
			default:
				say("502 no")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestSMTPSend(t *testing.T) {
	port, got := fakeSMTP(t, false)
	c := SMTP{Host: "127.0.0.1", Port: port, TLS: TLSNone}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), "grantra@example.com", "ana@example.com", []byte("Subject: hi\r\n\r\nhello\r\n")); err != nil {
		t.Fatal(err)
	}
	log := <-got
	for _, want := range []string{"MAIL FROM:<grantra@example.com>", "RCPT TO:<ana@example.com>", "Subject: hi", "hello"} {
		if !strings.Contains(log, want) {
			t.Errorf("server saw %q, want %q", log, want)
		}
	}
}

func TestSMTPRequiresStartTLS(t *testing.T) {
	port, _ := fakeSMTP(t, false)
	c := SMTP{Host: "127.0.0.1", Port: port, TLS: TLSStartTLS}
	err := c.Send(context.Background(), "grantra@example.com", "ana@example.com", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("send without STARTTLS = %v, want refused", err)
	}
}

func TestSMTPValidate(t *testing.T) {
	for _, c := range []SMTP{
		{Port: 25, TLS: TLSNone},
		{Host: "h", Port: 0, TLS: TLSNone},
		{Host: "h", Port: 25, TLS: "ssl"},
	} {
		if c.Validate() == nil {
			t.Errorf("%+v passed", c)
		}
	}
	if err := (SMTP{Host: "h", Port: 465, TLS: TLSImplicit}).Validate(); err != nil {
		t.Error(err)
	}
}
