// Package enroll runs the browser-loopback enrollment: it opens the Avora
// /agent/enroll page in the user's browser (where they're already signed in),
// then catches the device token the page redirects back to a local 127.0.0.1
// server. The token never leaves this machine over the network.
package enroll

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"avora-agent/internal/config"
)

const enrollTimeout = 5 * time.Minute

// Run performs the handshake and returns the device token.
func Run(cfg *config.Config, hostname, osName string) (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	state, err := nonce()
	if err != nil {
		return "", err
	}

	tokenCh := make(chan string, 1)
	srv := &http.Server{Handler: handler(state, tokenCh)}
	go func() { _ = srv.Serve(listener) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()

	enrollURL := fmt.Sprintf(
		"%s/agent/enroll?cb=%d&state=%s&host=%s&os=%s",
		cfg.FEBaseURL, port, state, url.QueryEscape(hostname), url.QueryEscape(osName),
	)
	fmt.Println("Opening your browser to connect this device...")
	fmt.Println("If it doesn't open, visit:\n  " + enrollURL)
	_ = openBrowser(enrollURL)

	select {
	case token := <-tokenCh:
		return token, nil
	case <-time.After(enrollTimeout):
		return "", fmt.Errorf("enrollment timed out after %s", enrollTimeout)
	}
}

// handler accepts the page's redirect, verifies the state nonce (so a stray
// page can't inject a token), and forwards the token.
func handler(state string, tokenCh chan<- string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state || q.Get("token") == "" {
			http.Error(w, "Invalid enrollment response.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(donePage))
		select {
		case tokenCh <- q.Get("token"):
		default:
		}
	})
	return mux
}

func nonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

const donePage = `<!doctype html><html><head><meta charset="utf-8"><title>Avora</title></head>` +
	`<body style="font-family:-apple-system,system-ui,sans-serif;text-align:center;padding-top:80px;color:#1a1530">` +
	`<h2>Device connected ✓</h2><p>You can close this tab and return to the Avora agent.</p></body></html>`
