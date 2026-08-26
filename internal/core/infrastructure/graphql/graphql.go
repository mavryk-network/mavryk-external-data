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
// here too: http.Client.Do wraps transport errors in a fresh *url.Error whose
// text embeds the full request URL (Go strips only userinfo, never the query),
// so the transport-level scrub alone cannot stop secrets carried as query
// params (e.g. the Equiteez ?bypass=) from reaching logs and persisted state.
var sensitiveQueryParams = map[string]struct{}{
	"bypass": {}, "key": {}, "token": {}, "secret": {},
	"password": {}, "api_key": {}, "apikey": {}, "access_token": {},
}

// scrubURLError rewrites sensitive query values inside an error produced by
// client.Do. Returns the original error untouched when nothing needs hiding;
// otherwise wraps it, preserving the errors.Is/As chain via Unwrap.
func scrubURLError(err error, u *url.URL) error {
	if err == nil || u == nil || u.RawQuery == "" {
		return err
	}
	q := u.Query()
	redacted := false
	for k := range q {
		if _, sensitive := sensitiveQueryParams[strings.ToLower(k)]; sensitive {
			q.Set(k, "REDACTED")
			redacted = true
		}
	}
	if !redacted {
		return err
	}
	raw := u.String()
	if !strings.Contains(err.Error(), raw) {
		return err
	}
	clone := *u
	clone.RawQuery = q.Encode()
	return &scrubbedError{msg: strings.ReplaceAll(err.Error(), raw, clone.String()), err: err}
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// maxErrorBodyLen bounds how much upstream body we embed in an error string.
// The body can be up to the outbound size cap (16 MiB); un-truncated it produces
// multi-megabyte single log lines that break log pipelines and can inject
// arbitrary content into structured logs.
const maxErrorBodyLen = 2048

func truncateForError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxErrorBodyLen {
		return s[:maxErrorBodyLen] + "…(truncated)"
	}
	return s
}
