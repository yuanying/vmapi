// Package client calls the vmapi API through devbox proxy, with a bearer
// token. It is what vmctl is built on.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/yuanying/vmapi/internal/api"
)

// Client calls one vmapi.
type Client struct {
	// BaseURL is where the API is, without /api/v1.
	BaseURL string
	// Token is the proxy's bearer token. It is sent and never shown.
	Token string
	HTTP  *http.Client
}

// Response is a successful answer: its status and its body as sent.
type Response struct {
	Status int
	Body   []byte
}

// Do sends the request. A body other than nil is sent as JSON. An answer
// that is not 2xx comes back as an *api.Error.
func (c *Client) Do(ctx context.Context, method, path string, body any) (*Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e api.ErrorBody
		if json.Unmarshal(data, &e) == nil && e.Error.Code != "" {
			return nil, &e.Error
		}
		// Not vmapi's answer: the proxy's, or something in between.
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "..."
		}
		return nil, &api.Error{Code: statusCode(resp.StatusCode), Message: fmt.Sprintf("%s %s: %s: %s", method, path, resp.Status, msg)}
	}
	return &Response{Status: resp.StatusCode, Body: data}, nil
}

// statusCode guesses the contract's code of an answer that has none.
func statusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return api.CodeInvalidArgument
	case http.StatusUnauthorized:
		return api.CodeUnauthenticated
	case http.StatusForbidden:
		return api.CodePermissionDenied
	case http.StatusNotFound:
		return api.CodeNotFound
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
		return api.CodeUnavailable
	default:
		return api.CodeInternal
	}
}

// VMPath is the path of a VM, or of something under it.
func VMPath(name string, rest ...string) string {
	p := "/api/v1/vms/" + url.PathEscape(name)
	for _, r := range rest {
		p += "/" + url.PathEscape(r)
	}
	return p
}
