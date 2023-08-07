/*
Package nanoleaf contains interfaces for interacting with
devices produced by Nanoleaf (https://nanoleaf.me/).
*/
package nanoleaf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const rawDebug = false

func debugf(format string, args ...interface{}) {
	if !rawDebug {
		return
	}
	for _, line := range strings.Split(fmt.Sprintf(format, args...), "\n") {
		fmt.Fprintf(os.Stderr, "\t| %s\n", line)
	}
}

type Controller struct {
	ip        string
	authToken string

	// Tracef, if set, will be used to write trace lines.
	Tracef func(ctx context.Context, format string, args ...interface{})
}

func (c *Controller) tracef(ctx context.Context, format string, args ...interface{}) {
	if c.Tracef != nil {
		c.Tracef(ctx, format, args...)
	}
}

func Connect(ip, authToken string) (*Controller, error) {
	// TODO: check connectivity?
	return &Controller{
		ip:        ip,
		authToken: authToken,
	}, nil
}

type State struct {
	Name            string `json:"name"` // the native name, not what you've given it
	Serial          string `json:"serialNo"`
	FirmwareVersion string `json:"firmwareVersion"`

	Effects struct {
		Selected string   `json:"select"`
		List     []string `json:"effectsList"`
	} `json:"effects"`

	// TODO: panelLayout, state
}

// State requests the state of the controller.
func (c *Controller) State(ctx context.Context) (*State, error) {
	var state State
	if err := c.get(ctx, "/", &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// Off turns the controller off.
func (c *Controller) Off(ctx context.Context) error {
	return c.put(ctx, "/state", struct {
		On struct {
			Value bool `json:"value"`
		} `json:"on"`
	}{})
}

// On turns the controller on.
func (c *Controller) On(ctx context.Context) error {
	req := struct {
		On struct {
			Value bool `json:"value"`
		} `json:"on"`
	}{}
	req.On.Value = true
	return c.put(ctx, "/state", req)
}

// SetBrightness sets the brightness to a value in [0,100], over a period of time.
func (c *Controller) SetBrightness(ctx context.Context, value int, dur time.Duration) error {
	req := struct {
		Brightness struct {
			Value    int `json:"value"`
			Duration int `json:"duration,omitempty"`
		} `json:"brightness"`
	}{}
	req.Brightness.Value = value
	if dur >= 0 {
		req.Brightness.Duration = int(dur / time.Second)
	}
	return c.put(ctx, "/state", req)
}

func (c *Controller) SetEffect(ctx context.Context, effect string) error {
	req := struct {
		Select string `json:"select"`
	}{Select: effect}
	return c.put(ctx, "/effects", req)
}

type Color struct {
	Hue        int // [0,360]
	Saturation int // [0,100]
	Brightness int // [0,100]
}

func (c *Controller) SetColor(ctx context.Context, col Color) error {
	var req struct {
		H struct {
			X int `json:"value"`
		} `json:"hue"`
		S struct {
			X int `json:"value"`
		} `json:"sat"`
		B struct {
			X int `json:"value"`
		} `json:"brightness"`
	}
	req.H.X, req.S.X, req.B.X = col.Hue, col.Saturation, col.Brightness
	return c.put(ctx, "/state", req)
}

func (c *Controller) api(token, path string) string {
	return "http://" + c.ip + ":16021/api/v1/" + token + path
}

// Automatic retry parameters.
//
// Doing GET and PUTs to a Nanoleaf controller on the LAN should usually be
// very quick, but they regularly fail, so we set strict timeouts and
// aggressively retry to improve reliability.
const (
	baseTimeout = 100 * time.Millisecond
	backoffMult = 1.5
	maxTimeout  = 5 * time.Second
)

type retryableOp func(context.Context) error

// retryableErr reports whether the error should cause another try.
func retryableErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return true
	}
	return false // any other error is probably permanent
}

func (c *Controller) retry(ctx context.Context, f retryableOp) error {
	// Classic exponential backoff.

	timeout := baseTimeout
	for {
		sub, cancel := context.WithTimeout(ctx, timeout)
		c.tracef(ctx, "Nanoleaf operation starting with timeout %v", timeout)
		debugf("Trying operation with timeout=%v", timeout)
		t0 := time.Now()
		err := f(sub)
		cancel()
		if !retryableErr(err) {
			// Success, or a non-timeout failure.
			c.tracef(ctx, "Nanoleaf operation finished after %v", time.Since(t0))
			debugf("Operation took %v", time.Since(t0))
			return err
		}
		if err := ctx.Err(); err != nil {
			// Give up on the overall effort.
			c.tracef(ctx, "Nanoleaf operation giving up")
			return err
		}
		// Try again.
		timeout = time.Duration(float64(timeout) * backoffMult)
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
	}
}

func (c *Controller) get(ctx context.Context, path string, dst interface{}) error {
	c.tracef(ctx, "Nanoleaf GET to %s", c.api("<tok>", path))
	debugf("GET to %s", c.api("<tok>", path))
	var resp *http.Response
	err := c.retry(ctx, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "GET", c.api(c.authToken, path), nil)
		if err != nil {
			return fmt.Errorf("preparing HTTP request: %w", err)
		}
		req.Close = true // If we need to retry, use a fresh connection.
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("making HTTP request: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	debugf("  %s\n  %s", resp.Status, body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP response %s", resp.Status)
	}
	if err != nil {
		return fmt.Errorf("reading HTTP response body: %w", err)
	}
	return json.Unmarshal(body, dst)
}

func (c *Controller) put(ctx context.Context, path string, obj interface{}) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("encoding JSON body: %w", err)
	}
	c.tracef(ctx, "Nanoleaf PUT to %s", c.api("<tok>", path))
	debugf("PUT to %s\n  %s", c.api("<tok>", path), body)
	var resp *http.Response
	err = c.retry(ctx, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "PUT", c.api(c.authToken, path), bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("preparing HTTP request: %w", err)
		}
		req.Close = true // If we need to retry, use a fresh connection.
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("making HTTP request: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	body, err = ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	debugf("  %s", resp.Status)
	if resp.StatusCode != 204 {
		debugf("  %s", body)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP response %s", resp.Status)
	}
	return nil
}
