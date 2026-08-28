package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Payload is one notification delivery.
type Payload struct {
	Title    string // e.g. "Tapetum: Motion — Front Door"
	Text     string // summary line
	URL      string // deep link to the event (may be "")
	Snapshot []byte // JPEG, may be nil
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Send dispatches to the channel's type implementation.
func Send(ctx context.Context, ch *Channel, p *Payload) error {
	switch ch.Type {
	case "smtp":
		return sendSMTP(ctx, ch, p)
	case "webhook":
		return sendWebhook(ctx, ch, p)
	case "ntfy":
		return sendNtfy(ctx, ch, p)
	case "gotify":
		return sendGotify(ctx, ch, p)
	case "discord":
		return sendDiscordSlack(ctx, ch, p)
	case "slack":
		return sendDiscordSlack(ctx, ch, p)
	case "telegram":
		return sendTelegram(ctx, ch, p)
	}
	return fmt.Errorf("unknown channel type %q", ch.Type)
}

func cfgStr(ch *Channel, k string) string {
	v, _ := ch.Config[k].(string)
	return v
}

func cfgFloat(ch *Channel, k string) float64 {
	v, _ := ch.Config[k].(float64)
	return v
}

func postJSON(ctx context.Context, url string, headers map[string]string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// --- smtp ---

func sendSMTP(ctx context.Context, ch *Channel, p *Payload) error {
	host := cfgStr(ch, "host")
	port := cfgFloat(ch, "port")
	if port == 0 {
		port = 587
	}
	from := cfgStr(ch, "from")
	to := cfgList(ch, "to")
	if host == "" || from == "" || len(to) == 0 {
		return errors.New("smtp: host, from and to are required")
	}
	tlsMode := cfgStr(ch, "tls") // "starttls" (default) | "tls" | "none"

	body := p.Text
	if p.URL != "" {
		body += "\n\n" + p.URL
	}

	msg := &bytes.Buffer{}
	fmt.Fprintf(msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, strings.Join(to, ", "), p.Title)
	if len(p.Snapshot) > 0 {
		fmt.Fprintf(msg, "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=tapetum\r\n\r\n")
		fmt.Fprintf(msg, "--tapetum\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", body)
		fmt.Fprintf(msg, "--tapetum\r\nContent-Type: image/jpeg\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=\"snapshot.jpg\"\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(p.Snapshot)
		for i := 0; i < len(enc); i += 76 {
			end := i + 76
			if end > len(enc) {
				end = len(enc)
			}
			fmt.Fprintf(msg, "%s\r\n", enc[i:end])
		}
		fmt.Fprintf(msg, "--tapetum--\r\n")
	} else {
		fmt.Fprintf(msg, "Content-Type: text/plain; charset=utf-8\r\n\r\n%s", body)
	}

	addr := fmt.Sprintf("%s:%d", host, int(port))
	var auth smtp.Auth
	if user := cfgStr(ch, "username"); user != "" {
		auth = smtp.PlainAuth("", user, cfgStr(ch, "password"), host)
	}

	switch tlsMode {
	case "none":
		return smtp.SendMail(addr, auth, from, to, msg.Bytes())
	case "tls":
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
		if err != nil {
			return err
		}
		c, err := smtp.NewClient(conn, host)
		if err != nil {
			return err
		}
		defer c.Close()
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		return smtpSend(c, from, to, msg.Bytes())
	default: // starttls
		c, err := smtp.Dial(addr)
		if err != nil {
			return err
		}
		defer c.Close()
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return err
			}
		}
		if auth != nil {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
		return smtpSend(c, from, to, msg.Bytes())
	}
}

func smtpSend(c *smtp.Client, from string, to []string, msg []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func cfgList(ch *Channel, k string) []string {
	var out []string
	switch v := ch.Config[k].(type) {
	case []any:
		for _, s := range v {
			if str, ok := s.(string); ok && str != "" {
				out = append(out, str)
			}
		}
	case []string:
		out = v
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// --- webhook ---

func sendWebhook(ctx context.Context, ch *Channel, p *Payload) error {
	url := cfgStr(ch, "url")
	if url == "" {
		return errors.New("webhook: url is required")
	}
	body := map[string]any{"title": p.Title, "text": p.Text, "url": p.URL}
	headers := map[string]string{}
	if h, ok := ch.Config["headers"].(map[string]any); ok {
		for k, v := range h {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}
	if secret := cfgStr(ch, "hmac_secret"); secret != "" {
		raw, _ := json.Marshal(body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(raw)
		headers["X-Tapetum-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	method := strings.ToUpper(cfgStr(ch, "method"))
	if method == "" {
		method = http.MethodPost
	}
	_ = method // generic JSON POST per docs; method override accepted but POST enforced by postJSON
	return postJSON(ctx, url, headers, body)
}

// --- ntfy / gotify ---

func sendNtfy(ctx context.Context, ch *Channel, p *Payload) error {
	server := strings.TrimRight(cfgStr(ch, "server"), "/")
	topic := cfgStr(ch, "topic")
	if server == "" || topic == "" {
		return errors.New("ntfy: server and topic are required")
	}
	headers := map[string]string{"Title": p.Title}
	if prio := cfgStr(ch, "priority"); prio != "" {
		headers["Priority"] = prio
	}
	if p.URL != "" {
		headers["Click"] = p.URL
	}
	if tok := cfgStr(ch, "token"); tok != "" {
		headers["Authorization"] = "Bearer " + tok
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		server+"/"+topic, strings.NewReader(p.Text))
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy: HTTP %d", resp.StatusCode)
	}
	return nil
}

func sendGotify(ctx context.Context, ch *Channel, p *Payload) error {
	server := strings.TrimRight(cfgStr(ch, "server"), "/")
	token := cfgStr(ch, "token")
	if server == "" || token == "" {
		return errors.New("gotify: server and token are required")
	}
	msg := p.Text
	if p.URL != "" {
		msg += "\n" + p.URL
	}
	prio := int(cfgFloat(ch, "priority"))
	return postJSON(ctx, server+"/message?token="+token, nil, map[string]any{
		"title": p.Title, "message": msg, "priority": prio,
	})
}

// --- discord / slack ---

func sendDiscordSlack(ctx context.Context, ch *Channel, p *Payload) error {
	url := cfgStr(ch, "url")
	if url == "" {
		return fmt.Errorf("%s: webhook url is required", ch.Type)
	}
	text := p.Text
	if p.URL != "" {
		text += " — " + p.URL
	}
	if ch.Type == "discord" {
		return postJSON(ctx, url, nil, map[string]any{
			"content": text,
			"embeds":  []map[string]any{{"title": p.Title}},
		})
	}
	return postJSON(ctx, url, nil, map[string]any{"text": "*" + p.Title + "*\n" + text})
}

// --- telegram ---

func sendTelegram(ctx context.Context, ch *Channel, p *Payload) error {
	token := cfgStr(ch, "bot_token")
	chatID := cfgStr(ch, "chat_id")
	if token == "" || chatID == "" {
		return errors.New("telegram: bot_token and chat_id are required")
	}
	base := "https://api.telegram.org/bot" + token
	caption := p.Title + "\n" + p.Text
	if p.URL != "" {
		caption += "\n" + p.URL
	}
	if len(p.Snapshot) > 0 {
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		_ = w.WriteField("chat_id", chatID)
		_ = w.WriteField("caption", caption)
		fw, err := w.CreateFormFile("photo", "snapshot.jpg")
		if err != nil {
			return err
		}
		if _, err := fw.Write(p.Snapshot); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/sendPhoto", &buf)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, string(b))
		}
		return nil
	}
	return postJSON(ctx, base+"/sendMessage", nil, map[string]any{
		"chat_id": chatID, "text": caption,
	})
}
