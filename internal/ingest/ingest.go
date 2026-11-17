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

// ErrUnauthorized means the device token was rejected (revoked or invalid) - a
// 401 from any endpoint. The runner treats it as a claim to verify over time
// (see runner.recordAuth), never as an instant revocation.
var ErrUnauthorized = errors.New("device token rejected (revoked or invalid)")

// StatusError is any other non-success HTTP reply. The status code is what the
// outbox needs to decide between retrying later and giving up on a sample.
type StatusError struct {
	Op   string // e.g. "ingest", "screenshot upload"
	Code int
	Body string // trimmed server reply; may echo the request, so never report it upstream
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s failed (%d %s): %s", e.Op, e.Code, http.StatusText(e.Code), e.Body)
}

// Retryable reports whether err is worth sending again later.
//
// Transport errors (offline, DNS, timeout), 5xx, 429 and 408 are transient. So
// are 401 (the runner's dormancy logic owns that decision) and 403, which is
// what this backend answers when the per-device rate limit trips. Every other
// 4xx means the server looked at this exact request and refused it, and will
// refuse it again, so retrying would only wedge the queue behind it.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnauthorized) {
		return true
	}
	var se *StatusError
	if !errors.As(err, &se) {
		return true // transport-level failure
	}
	switch {
	case se.Code >= 500:
		return true
	case se.Code == http.StatusTooManyRequests,
		se.Code == http.StatusRequestTimeout,
		se.Code == http.StatusForbidden:
		return true
	}
	return false
}

// MaybeDelivered reports whether a failed request might nevertheless have been
// committed by the server: the connection broke or the server erred after
// receiving the body. A 401/403/429 is decided before the handler runs, so it
// never was.
func MaybeDelivered(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, ErrUnauthorized) {
		return false
	}
	var se *StatusError
	if !errors.As(err, &se) {
		return true
	}
	return se.Code >= 500
}

// Short is err without any server-supplied text, for reporting upstream (the
// server's reply can echo the request, which may hold titles or URLs).
func Short(err error) string {
	var se *StatusError
	if errors.As(err, &se) {
		return fmt.Sprintf("%s failed (HTTP %d)", se.Op, se.Code)
	}
	return err.Error()
}

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

// Send posts one sample. Returns nil on 202, ErrReplay on 409, ErrUnauthorized
// on 401 and a *StatusError otherwise. capturedAt is when the sample was taken,
// which differs from now when it is replayed from the offline queue; the server
// records it as the client timestamp (and flags it as skew), never trusts it.
func Send(client *http.Client, cfg *config.Config, sequence int, s collect.Sample, capturedAt time.Time) error {
	body := payload{
		Sequence:        sequence,
		ClientTimestamp: capturedAt.UTC().Format(time.RFC3339),
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
	code, msg, err := postSigned(client, cfg, "/api/v1/activity/ingest", "application/json", raw, nil)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return ErrReplay
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return &StatusError{Op: "ingest", Code: code, Body: msg}
	}
}

// maxReplyBytes bounds how much of an error reply is kept for the log.
const maxReplyBytes = 4 << 10

// postSigned POSTs body with the device's bearer token and an HMAC-SHA256 of
// the exact body bytes in X-Signature, and returns the status code and the
// trimmed reply. A non-nil error means no HTTP reply was received at all.
func postSigned(
	client *http.Client, cfg *config.Config, path, contentType string, body []byte, headers map[string]string,
) (int, string, error) {
	mac := hmac.New(sha256.New, []byte(cfg.DeviceToken))
	_, _ = mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequest(http.MethodPost, cfg.APIBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+cfg.DeviceToken)
	req.Header.Set("X-Signature", signature)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	return resp.StatusCode, strings.TrimSpace(string(msg)), nil
}

// ErrNotFound means the endpoint does not exist on this backend (an older
// deployment). Only the health report expects it.
var ErrNotFound = errors.New("endpoint not found")

// SendHealth posts the agent's capture-health report (any JSON-encodable
// value) to /api/v1/devices/health with the usual device auth. Any 2xx is
// success; 404 is ErrNotFound so the caller can recognise an older backend.
func SendHealth(client *http.Client, cfg *config.Config, report any) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	code, msg, err := postSigned(client, cfg, "/api/v1/devices/health", "application/json", raw, nil)
	if err != nil {
		return err
	}
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return ErrUnauthorized
	case code == http.StatusNotFound:
		return ErrNotFound
	default:
		return &StatusError{Op: "health report", Code: code, Body: msg}
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
// the raw image. capturedAt is when the screen was grabbed (it differs from now
// for a screenshot replayed from the offline queue).
func SendScreenshot(client *http.Client, cfg *config.Config, shot capture.Shot, capturedAt time.Time) error {
	headers := map[string]string{
		"X-Captured-At": capturedAt.UTC().Format(time.RFC3339),
		"X-Width":       strconv.Itoa(shot.Width),
		"X-Height":      strconv.Itoa(shot.Height),
	}
	// Per-monitor rectangles within the combined image, "x,y,w,h;..." - lets the
	// backend OCR each screen separately. Metadata only (unsigned, like W/H); the
	// server re-validates it against the image bounds.
	if len(shot.Monitors) > 0 {
		parts := make([]string, len(shot.Monitors))
		for i, m := range shot.Monitors {
			parts[i] = fmt.Sprintf("%d,%d,%d,%d", m.X, m.Y, m.W, m.H)
		}
		headers["X-Monitors"] = strings.Join(parts, ";")
	}

	code, msg, err := postSigned(client, cfg, "/api/v1/screenshots", "image/jpeg", shot.JPEG, headers)
	if err != nil {
		return err
	}
	switch code {
	case http.StatusAccepted:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return &StatusError{Op: "screenshot upload", Code: code, Body: msg}
	}
}
