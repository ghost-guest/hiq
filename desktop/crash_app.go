package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
	"time"
	"unicode/utf8"
)

// crash_app.go is the crash/feedback reporting surface. Reports are sent only on
// an explicit user click in the frontend crash overlay — never automatically.

var crashEndpoint = "https://crash.hiq.io/v1/report"

const maxCrashDetailBytes = 16 << 10

var userPathSegment = regexp.MustCompile(`(?i)([A-Z]:\\Users\\|/(?:home|Users)/)[^/\\:\s"']+`)

func scrubUserPaths(s string) string {
	return userPathSegment.ReplaceAllString(s, "${1}_")
}

type crashReport struct {
	Kind    string     `json:"kind"`
	Version string     `json:"version"`
	OS      string     `json:"os"`
	Arch    string     `json:"arch"`
	Message string     `json:"message"`
	Device  deviceInfo `json:"device"`
}

func (a *App) ReportCrash(kind, detail string) error {
	if kind != "crash" && kind != "feedback" {
		return fmt.Errorf("unknown report kind %q", kind)
	}
	if detail == "" {
		return fmt.Errorf("empty report")
	}
	if len(detail) > maxCrashDetailBytes {
		// Truncate at a rune boundary so we never emit invalid UTF-8 (slicing
		// mid-multibyte would corrupt CJK text and turn json.Marshal's output
		// into U+FFFD replacement noise — the original crash text matters most).
		n := maxCrashDetailBytes
		for n > 0 && !utf8.RuneStart(detail[n]) {
			n--
		}
		detail = detail[:n]
	}
	c, err := httpClient()
	if err != nil {
		return err
	}
	return postCrashReport(a.reqCtx(), c, crashEndpoint, crashReport{
		Kind:    kind,
		Version: version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Message: scrubUserPaths(detail),
		Device:  collectDeviceInfo(),
	})
}

func postCrashReport(ctx context.Context, c *http.Client, endpoint string, r crashReport) error {
	// Bound the upload so a hung/slow endpoint can't pin the user's crash-report
	// click forever (ctx comes from Wails app ctx with no deadline). 10s is ample
	// for a <16KB payload. See audit finding E6.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("crash endpoint returned %s", resp.Status)
	}
	return nil
}
