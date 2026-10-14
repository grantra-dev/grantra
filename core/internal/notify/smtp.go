package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// TLS modes for SMTP.
const (
	// TLSStartTLS upgrades a plain connection (usually port 587) and refuses
	// servers that cannot.
	TLSStartTLS = "starttls"
	// TLSImplicit connects over TLS from the start (usually port 465).
	TLSImplicit = "tls"
	// TLSNone sends in the clear, for a mail catcher on the same machine.
	// Passwords are still never sent over it except to localhost.
	TLSNone = "none"
)

// SMTP sends mail through an SMTP server.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      string
	// Timeout bounds one whole send. Zero means 30 seconds.
	Timeout time.Duration
}

// Validate checks the settings.
func (c SMTP) Validate() error {
	if c.Host == "" {
		return errors.New("smtp host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("smtp port %d is not valid", c.Port)
	}
	switch c.TLS {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return fmt.Errorf("smtp tls must be %q, %q or %q", TLSStartTLS, TLSImplicit, TLSNone)
	}
	return nil
}

// Send delivers one message to one recipient.
func (c SMTP) Send(ctx context.Context, from, to string, msg []byte) error {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return err
		}
	}
	tlsConfig := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.TLS == TLSImplicit {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		return err
	}
	defer client.Close()
	if c.TLS == TLSStartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the smtp server does not offer STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if c.Username != "" {
		// PlainAuth refuses to send the password over a plain connection,
		// except to localhost.
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}
