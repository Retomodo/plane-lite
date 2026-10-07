// Package mail sends Plane's transactional emails over SMTP. Templates are
// copied verbatim from the Django backend (apps/api/templates/emails).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"time"

	"plane-lite/server/internal/config"
)

//go:embed templates
var templateFS embed.FS

var (
	varRe      = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	styleRe    = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]*>`)
	blankRunRe = regexp.MustCompile(`\n\s*\n\s*\n+`)
	escaper    = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;")
)

// Render fills a Django template that uses only {{ var }} substitutions,
// HTML-escaping values the way Django's autoescape does.
func Render(name string, vars map[string]string) (string, error) {
	b, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		return "", err
	}
	var missing []string
	out := varRe.ReplaceAllStringFunc(string(b), func(m string) string {
		key := varRe.FindStringSubmatch(m)[1]
		v, ok := vars[key]
		if !ok {
			missing = append(missing, key)
		}
		return escaper.Replace(v)
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("mail: template %s: missing vars %v", name, missing)
	}
	return out, nil
}

// PlainText ports plane.utils.email.generate_plain_text_from_html.
func PlainText(htmlContent string) string {
	s := styleRe.ReplaceAllString(htmlContent, "")
	s = commentRe.ReplaceAllString(s, "")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = blankRunRe.ReplaceAllString(s, "\n\n")
	return "\n\n" + strings.TrimSpace(s) + "\n\n"
}

type Mailer struct {
	cfg config.Email
}

func New(cfg config.Email) *Mailer { return &Mailer{cfg: cfg} }

// Configured reports whether SMTP is set up (Plane's is_smtp_configured).
func (m *Mailer) Configured() bool { return m.cfg.Configured() }

// SendTemplate renders an HTML template and sends it with a derived
// plain-text alternative, like Plane's EmailMultiAlternatives usage.
func (m *Mailer) SendTemplate(ctx context.Context, to, subject, template string, vars map[string]string) error {
	htmlBody, err := Render(template, vars)
	if err != nil {
		return err
	}
	return m.Send(ctx, []string{to}, subject, PlainText(htmlBody), htmlBody)
}

// Send delivers one message, honouring EMAIL_USE_TLS (STARTTLS) and
// EMAIL_USE_SSL (implicit TLS) like Django's SMTP backend.
func (m *Mailer) Send(ctx context.Context, to []string, subject, text, htmlBody string) error {
	from, err := mail.ParseAddress(m.cfg.From)
	if err != nil {
		return fmt.Errorf("mail: EMAIL_FROM: %w", err)
	}
	msg, err := buildMessage(m.cfg.From, from.Address, to, subject, text, htmlBody)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	dialer := &net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	if m.cfg.UseSSL {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: m.cfg.Host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mail: dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: %w", err)
	}
	defer c.Close()
	if m.cfg.UseTLS && !m.cfg.UseSSL {
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if m.cfg.User != "" {
		if err := c.Auth(loginAuth{m.cfg.User, m.cfg.Password, m.cfg.Host}); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("mail: RCPT TO %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: send: %w", err)
	}
	return c.Quit()
}

// loginAuth is PLAIN auth without net/smtp's refusal on unencrypted
// connections: Django's backend logs in whenever credentials are set, and
// the operator chose whether to enable TLS.
type loginAuth struct{ user, pass, host string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (a loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("mail: unexpected server challenge")
	}
	return nil, nil
}

func buildMessage(fromHeader, fromAddr string, to []string, subject, text, htmlBody string) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	var id [12]byte
	_, _ = rand.Read(id[:])
	domain := fromAddr[strings.LastIndex(fromAddr, "@")+1:]

	hdr := []string{
		"From: " + encodeAddressHeader(fromHeader),
		"To: " + strings.Join(to, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id[:]) + "@" + domain + ">",
		"MIME-Version: 1.0",
		`Content-Type: multipart/alternative; boundary="` + mw.Boundary() + `"`,
	}
	head := strings.Join(hdr, "\r\n") + "\r\n\r\n"

	for _, part := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", text},
		{"text/html; charset=utf-8", htmlBody},
	} {
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return append([]byte(head), buf.Bytes()...), nil
}

func encodeAddressHeader(s string) string {
	a, err := mail.ParseAddress(s)
	if err != nil {
		return s
	}
	return a.String()
}
