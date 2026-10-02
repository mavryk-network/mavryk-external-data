package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Execute runs a GraphQL request over HTTP POST and returns the JSON of the
// top-level "data" field. Non-empty "errors" in the response body yields an error.
func Execute(ctx context.Context, client *http.Client, serviceName, url, query string, variables map[string]interface{}, headers map[string]string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if variables == nil {
		variables = map[string]interface{}{}
	}
	payload, err := json.Marshal(map[string]interface{}{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("new graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	ua := "quotes-service/1.0"
	if serviceName != "" {
		ua = fmt.Sprintf("quotes-service/1.0 (%s)", serviceName)
	}
	req.Header.Set("User-Agent", ua)
	for k, v := range headers {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, scrubURLError(err, req.URL)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("graphql request failed: status %d: %s", resp.StatusCode, truncateForError(string(body)))
	}

	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode graphql response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("graphql returned errors: %s", truncateForError(string(bytes.TrimSpace(envelope.Errors[0]))))
	}
	if len(envelope.Data) == 0 {
		return nil, fmt.Errorf("graphql response has empty data")
	}
	return []byte(envelope.Data), nil
}

// sensitiveQueryParams mirror the logging transport's redaction list. Needed
// here too: client.Do wraps transport errors in a fresh *url.Error embedding the
// full request URL, and Go strips only userinfo, never the query.
var sensitiveQueryParams = map[string]struct{}{
	"bypass": {}, "key": {}, "token": {}, "secret": {},
	"password": {}, "api_key": {}, "apikey": {}, "access_token": {},
}

// scrubURLError rewrites sensitive query values inside an error from client.Do,
// preserving the errors.Is/As chain via Unwrap.
func scrubURLError(err error, u *url.URL) error {
	if err == nil || u == nil || u.RawQuery == "" {
		return err
	}
	q := u.Query()
	var secrets []string
	for k, vs := range q {
		if _, sensitive := sensitiveQueryParams[strings.ToLower(k)]; !sensitive {
			continue
		}
		for _, v := range vs {
			if v != "" {
				secrets = append(secrets, v)
			}
		}
		q.Set(k, "REDACTED")
	}
	if len(secrets) == 0 {
		return err
	}
	msg := err.Error()
	if raw := u.String(); strings.Contains(msg, raw) {
		clone := *u
		clone.RawQuery = q.Encode()
		msg = strings.ReplaceAll(msg, raw, clone.String())
	}
	// A same-host redirect can hand Do a different URL than we built, so scrub
	// the secret VALUES themselves too, both spellings.
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "REDACTED")
		if enc := url.QueryEscape(s); enc != s {
			msg = strings.ReplaceAll(msg, enc, "REDACTED")
		}
	}
	if msg == err.Error() {
		return err
	}
	return &scrubbedError{msg: msg, err: err}
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// maxErrorBodyLen bounds how much upstream body an error string embeds: the body
// can reach the 16 MiB outbound cap, and un-truncated it breaks log pipelines and
// can inject arbitrary content into structured logs.
const maxErrorBodyLen = 2048

func truncateForError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorBodyLen {
		return s[:maxErrorBodyLen] + "…(truncated)"
	}
	return s
}
