// Package ingest signs and posts a single activity sample to the backend. The
// body is HMAC-SHA256 signed with the device token (the exact bytes that are
// sent are the bytes that are signed) and carried in the X-Signature header,
// matching the backend's verify_device contract.
package ingest

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"avora-agent/internal/capture"
	"avora-agent/internal/collect"
	"avora-agent/internal/config"
)

// ErrReplay means the server rejected the sequence as reused/out-of-order (409).
var ErrReplay = errors.New("sequence rejected as replay")

// ErrUnauthorized means the device token was rejected (revoked or invalid) — a
// 401 from any endpoint. The agent treats it as a signal to stop and de-enroll.
var ErrUnauthorized = errors.New("device token rejected (revoked or invalid)")

// payload mirrors the backend ActivityIngest schema, which forbids extra keys.
type payload struct {
	Sequence        int     `json:"sequence"`
	ClientTimestamp string  `json:"client_timestamp"`
	ActiveWindow    *string `json:"active_window,omitempty"`
	IdleSeconds     int     `json:"idle_seconds"`
	URL             *string `json:"url,omitempty"`
	PageTitle       *string `json:"page_title,omitempty"`
	Browser         *string `json:"browser,omitempty"`
}

// Send posts one sample. Returns nil on 202, ErrReplay on 409.
func Send(client *http.Client, cfg *config.Config, sequence int, s collect.Sample) error {
	body := payload{
		Sequence:        sequence,
		ClientTimestamp: time.Now().UTC().Format(time.RFC3339),
		IdleSeconds:     s.IdleSeconds,
	}
	if s.ActiveWindow != "" {
		body.ActiveWindow = &s.ActiveWindow
	}
	if s.URL != "" {
		body.URL = &s.URL
	}
	if s.PageTitle != "" {
		body.PageTitle = &s.PageTitle
	}
	if s.Browser != "" {
		body.Browser = &s.Browser
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}

	mac := hmac.New(sha256.New, []byte(cfg.DeviceToken))
	_, _ = mac.Write(raw)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(
		http.MethodPost, cfg.APIBaseURL+"/api/v1/activity/ingest", bytes.NewReader(raw),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
	req.Header.Set("X-Signature", signature)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return ErrReplay
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return fmt.Errorf("ingest failed (%s): %s", resp.Status, strings.TrimSpace(string(msg)))
	}
}

// Ping is a downlink command from a manager/admin. Kind is "ping" (sound +
// message) or "capture" (take a screenshot now).
type Ping struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// FetchPings polls the agent's inbox. It's a GET, so the HMAC is over an empty
// body. The backend marks returned pings delivered, so each is seen once.
func FetchPings(client *http.Client, cfg *config.Config) ([]Ping, error) {
	req, err := http.NewRequest(http.MethodGet, cfg.APIBaseURL+"/api/v1/pings/pending", nil)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(cfg.DeviceToken))
	req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
	req.Header.Set("X-Signature", hex.EncodeToString(mac.Sum(nil))) // HMAC of empty body

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("pings fetch failed (%s): %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var pings []Ping
	if err := json.NewDecoder(resp.Body).Decode(&pings); err != nil {
		return nil, err
	}
	return pings, nil
}

// SendScreenshot uploads a captured screenshot. The image bytes are HMAC-signed
// (same scheme as activity); metadata rides in headers since the signed body is
// the raw image.
func SendScreenshot(client *http.Client, cfg *config.Config, shot capture.Shot) error {
	mac := hmac.New(sha256.New, []byte(cfg.DeviceToken))
	_, _ = mac.Write(shot.JPEG)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(
		http.MethodPost, cfg.APIBaseURL+"/api/v1/screenshots", bytes.NewReader(shot.JPEG),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
	req.Header.Set("X-Signature", signature)
	req.Header.Set("X-Captured-At", time.Now().UTC().Format(time.RFC3339))
	req.Header.Set("X-Width", strconv.Itoa(shot.Width))
	req.Header.Set("X-Height", strconv.Itoa(shot.Height))
	// Per-monitor rectangles within the combined image, "x,y,w,h;…" — lets the
	// backend OCR each screen separately. Metadata only (unsigned, like W/H); the
	// server re-validates it against the image bounds.
	if len(shot.Monitors) > 0 {
		parts := make([]string, len(shot.Monitors))
		for i, m := range shot.Monitors {
			parts[i] = fmt.Sprintf("%d,%d,%d,%d", m.X, m.Y, m.W, m.H)
		}
		req.Header.Set("X-Monitors", strings.Join(parts, ";"))
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	msg, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("screenshot upload failed (%s): %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
