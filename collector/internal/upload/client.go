// Package upload talks to the collector server API: enroll, invite and ingest
// (docs/agents/SPEC.md "Wire format"). Auth is the X-D0M1-Token header, never
// Authorization, because SWA overwrites that one.
package upload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/7-of-9/tokenmaxr/collector/internal/buildinfo"
	"github.com/7-of-9/tokenmaxr/collector/internal/model"
	"github.com/7-of-9/tokenmaxr/collector/internal/outbox"
)

const (
	TokenHeader = "X-D0M1-Token"
	// Timeout covers managed Functions cold starts (10-30 s).
	Timeout = 60 * time.Second
	// MaxBody is the server's request cap.
	MaxBody = 1 << 20
)

// StatusError is a non-2xx response.
type StatusError struct {
	Code int
	Msg  string
}

func (e *StatusError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("HTTP %d", e.Code)
}

// Code returns the HTTP status of err, or 0 for transport errors.
func Code(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

type Client struct {
	Endpoint string // e.g. https://your-server.example
	Token    string
	Version  string
	HTTP     *http.Client
}

func NewClient(endpoint, token, version string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
		Version:  version,
		HTTP:     &http.Client{Timeout: Timeout},
	}
}

func (c *Client) post(ctx context.Context, path string, body []byte, out any) error {
	return c.do(ctx, http.MethodPost, path, body, out)
}

// do sends one request (a JSON body unless body is nil) and decodes a 2xx
// response into out (nil: ignored).
func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", buildinfo.Product+"/"+c.Version)
	if c.Token != "" {
		req.Header.Set(TokenHeader, c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode, Msg: errorMessage(data)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("bad response from %s: %w", path, err)
	}
	return nil
}

// errorMessage extracts {"error": "..."} or a short plain-text body.
func errorMessage(data []byte) string {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Valid(data) {
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return e.Error
		}
		return e.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 200 || strings.HasPrefix(s, "<") {
		return ""
	}
	return s
}

// rejectedItem is one entry of the response's "rejected" list: an item the
// server found invalid and will never take (SPEC "POST /api/ingest"). The id
// is empty for a bad heartbeat.
type rejectedItem struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// ingestResponse is the shared wire type plus the fields only the uploader
// reads (model.IngestResponse is the contract shared with the API and stays
// as it is).
type ingestResponse struct {
	model.IngestResponse
	Rejected []rejectedItem `json:"rejected"`
}

// Heartbeat is the heartbeat as sent: the shared wire type plus the homes
// scanned (SPEC "Scan roots"; the API keeps them privately on the machine
// row). model.Heartbeat is the frozen contract and stays as it is.
type Heartbeat struct {
	*model.Heartbeat
	Homes []string `json:"homes,omitempty"`
}

// Limits the API applies to the homes list (api/src/lib/validate.js).
const (
	MaxHomes     = 16
	MaxHomeChars = 260
)

// CapHomes drops paths the API would reject, so one long path cannot cost
// the whole heartbeat.
func CapHomes(homes []string) []string {
	var out []string
	for _, h := range homes {
		if len(h) > MaxHomeChars || len(out) >= MaxHomes {
			continue
		}
		out = append(out, h)
	}
	return out
}

// ingestRequest is the wire request with the heartbeat wrapper in place of
// the shared field (the outer field wins when encoding).
type ingestRequest struct {
	model.IngestRequest
	Heartbeat *Heartbeat `json:"heartbeat,omitempty"`
}

func newIngestRequest(version string, sentAt time.Time, b outbox.Batch, hb *model.Heartbeat, homes []string) *ingestRequest {
	req := &ingestRequest{IngestRequest: model.IngestRequest{
		V:                model.WireVersion,
		CollectorVersion: version,
		SentAt:           sentAt,
		Usage:            b.Usage,
		Activity:         b.Activity,
		Prompts:          b.Prompts,
		Limits:           b.Limits,
		AccountUsage:     b.AccountUsage,
	}}
	if hb != nil {
		req.Heartbeat = &Heartbeat{Heartbeat: hb, Homes: CapHomes(homes)}
	}
	return req
}

func (c *Client) Ingest(ctx context.Context, req *model.IngestRequest) (model.IngestResponse, error) {
	out, err := c.ingest(ctx, req)
	return out.IngestResponse, err
}

// SendHeartbeat posts a heartbeat alone (the label command).
func (c *Client) SendHeartbeat(ctx context.Context, version string, sentAt time.Time, hb *model.Heartbeat, homes []string) error {
	_, err := c.ingest(ctx, newIngestRequest(version, sentAt, outbox.Batch{}, hb, homes))
	return err
}

func (c *Client) ingest(ctx context.Context, req any) (ingestResponse, error) {
	var out ingestResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	err = c.post(ctx, "/api/ingest", body, &out)
	return out, err
}

func (c *Client) Enroll(ctx context.Context, req model.EnrollRequest) (model.EnrollResponse, error) {
	var out model.EnrollResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	err = c.post(ctx, "/api/enroll", body, &out)
	if err == nil && (out.MachineID == "" || out.Token == "") {
		err = errors.New("enroll response is missing machineId or token")
	}
	return out, err
}

func (c *Client) Invite(ctx context.Context) (model.InviteResponse, error) {
	var out model.InviteResponse
	err := c.post(ctx, "/api/invite", []byte("{}"), &out)
	if err == nil && out.Invite == "" {
		err = errors.New("invite response is missing the invite")
	}
	return out, err
}

// FleetGitHub is the fleet's shared GitHub sign-in as the server keeps it
// (SPEC "GET /api/fleet/github"): Blob is opaque to the server, sealed by
// the collectors with the fleet key (internal/fleetshare); By is the
// server machine id that shared it.
type FleetGitHub struct {
	Blob      string    `json:"blob"`
	UpdatedAt time.Time `json:"updatedAt"`
	By        string    `json:"by"`
}

const fleetGitHubPath = "/api/fleet/github"

// GetFleetGitHub returns the fleet's shared GitHub sign-in, or nil when
// there is none (404).
func (c *Client) GetFleetGitHub(ctx context.Context) (*FleetGitHub, error) {
	var out FleetGitHub
	err := c.do(ctx, http.MethodGet, fleetGitHubPath, nil, &out)
	if Code(err) == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.Blob == "" {
		return nil, errors.New("fleet sign-in response has no blob")
	}
	return &out, nil
}

// FleetGitHubPut is the server's answer to a share.
type FleetGitHubPut struct {
	UpdatedAt time.Time `json:"updatedAt"`
	// Escrowed: the server keeps the fleet key (SPEC "Linking a machine"),
	// so whoever runs it could open the share.
	Escrowed bool `json:"escrowed"`
}

// PutFleetGitHub shares this machine's sealed GitHub sign-in with the fleet,
// replacing any other.
func (c *Client) PutFleetGitHub(ctx context.Context, blob string) (FleetGitHubPut, error) {
	var out FleetGitHubPut
	body, err := json.Marshal(map[string]string{"blob": blob})
	if err != nil {
		return out, err
	}
	err = c.do(ctx, http.MethodPut, fleetGitHubPath, body, &out)
	return out, err
}

// DeleteFleetGitHub withdraws the fleet's shared GitHub sign-in (deleting
// one that is gone is not an error).
func (c *Client) DeleteFleetGitHub(ctx context.Context) error {
	err := c.do(ctx, http.MethodDelete, fleetGitHubPath, nil, nil)
	if Code(err) == http.StatusNotFound {
		return nil
	}
	return err
}
