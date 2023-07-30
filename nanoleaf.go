/*
Package nanoleaf contains interfaces for interacting with
devices produced by Nanoleaf (https://nanoleaf.me/).
*/
package nanoleaf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

type Controller struct {
	ip        string
	authToken string
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

func (c *Controller) SetEffect(ctx context.Context, effect string) error {
	req := struct {
		Select string `json:"select"`
	}{Select: effect}
	return c.put(ctx, "/effects", req)
}

func (c *Controller) get(ctx context.Context, path string, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+c.ip+":16021/api/v1/"+c.authToken+path, nil)
	if err != nil {
		return fmt.Errorf("preparing HTTP request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("making HTTP request: %w", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	// TODO: body to capture?
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
	req, err := http.NewRequestWithContext(ctx, "PUT", "http://"+c.ip+":16021/api/v1/"+c.authToken+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("preparing HTTP request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("making HTTP request: %w", err)
	}
	// TODO: body to capture?
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP response %s", resp.Status)
	}
	return nil
}
