package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"swol/config"
)

func strptr(s string) *string { return &s }

// fakePinger is a test Pinger that returns canned reachability per address.
type fakePinger struct {
	reachable map[string]bool
}

func (f fakePinger) Reachable(addr string) (bool, error) {
	return f.reachable[addr], nil
}

func TestHandleIndexRendersMachines(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{{Name: "alpha", Mac: "01:02:03:04:05:06"}},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "alpha")
}

func TestHandleIndexVisibilityAndCardOrder(t *testing.T) {
	cfg := &config.Config{Machines: []config.Machine{
		{Name: "visible", Icon: "laptop", Mac: "01:02:03:04:05:06", IP: strptr("192.168.1.10"), SOLEnabled: true},
		{Name: "hidden", Mac: "0a:0b:0c:0d:0e:0f", IP: strptr("192.168.1.11"),
			ShowMAC: configBoolptr(false), ShowIP: configBoolptr(false), ShowDetails: configBoolptr(false)},
	}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	parts := strings.Split(body, `<li class="machine"`)
	require.Len(t, parts, 3)
	visible, hidden := parts[1], parts[2]

	assert.Contains(t, visible, `class="machine__name-details"`)
	assert.Contains(t, visible, `class="bi bi-laptop machine__icon"`)
	assert.Contains(t, visible, `class="machine__details-data mono private-details__plain"`)
	assert.Contains(t, visible, `class="machine__details-data mono private-details__masked"`)
	assert.Contains(t, visible, `class="machine__property-label">IP:`)
	assert.Contains(t, visible, `class="machine__property-label">MAC:`)
	assert.Contains(t, visible, `192.168.1.10`)
	assert.Contains(t, visible, `01:02:03:04:05:06`)

	name := strings.Index(visible, `class="machine__identity"`)
	ip := strings.Index(visible, `class="machine__property-label">IP:`)
	mac := strings.Index(visible, `class="machine__property-label">MAC:`)
	wake := strings.Index(visible, `class="machine__power-button machine__power-button--wake"`)
	sleep := strings.Index(visible, `class="machine__power-button machine__power-button--sleep"`)
	assert.True(t, name >= 0 && name < ip && ip < mac && mac < wake && wake < sleep,
		"expected name -> IP -> MAC -> Wake -> Sleep, got: %d %d %d %d %d", name, ip, mac, wake, sleep)

	assert.NotContains(t, hidden, `<details class="machine__name-details">`)
	assert.NotContains(t, hidden, `class="machine__property-label">IP:`)
	assert.NotContains(t, hidden, `class="machine__property-label">MAC:`)
	assert.NotContains(t, hidden, `class="machine__details-data mono`)
	assert.Contains(t, hidden, `<span class="machine__name-text">hidden</span>`)
	assert.Contains(t, hidden, `class="machine__power-button machine__power-button--wake"`)
}

func TestHandleIndexBootstrapIconsAndPrivacyToggle(t *testing.T) {
	cfg := &config.Config{Machines: []config.Machine{{Name: "laptop", Mac: "01:02:03:04:05:06", Icon: "bi-laptop"}}}
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `href="/static/bootstrap-icons.css"`)
	assert.Contains(t, body, `class="bi bi-laptop machine__icon"`)
	assert.Contains(t, body, `id="address-privacy-toggle"`)
	assert.Contains(t, body, `class="private-value__mask"`)
	assert.NotContains(t, body, "window.confirm(")
	assert.NotContains(t, body, `<dialog`)
	assert.NotContains(t, body, `class="machine__actions-trigger"`)
	assert.Contains(t, body, `<html lang="en" data-addresses-masked="true">`)
	assert.Contains(t, body, `let masked = true;`)
	assert.Contains(t, body, `SWOL`)
	assert.Contains(t, body, `href="/static/swol.svg"`)
	assert.NotContains(t, body, `footer__version`)
	assert.NotContains(t, body, `built {{.Date}}`)
	assert.Contains(t, body, `border-radius: 12px 0 0 12px;`)
	assert.Contains(t, body, `border-radius: 0 12px 12px 0;`)
	assert.Contains(t, body, `align-items: start;`)

	css := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(css, httptest.NewRequest(http.MethodGet, "/static/bootstrap-icons.css", nil))
	require.Equal(t, http.StatusOK, css.Code)
	assert.Contains(t, css.Body.String(), ".bi-eye::before")

	font := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(font, httptest.NewRequest(http.MethodGet, "/static/fonts/bootstrap-icons.woff2", nil))
	require.Equal(t, http.StatusOK, font.Code)
	assert.Greater(t, font.Body.Len(), 0)

	redirect := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(redirect, httptest.NewRequest(http.MethodGet, "/favicon.ico", nil))
	assert.Equal(t, http.StatusPermanentRedirect, redirect.Code)
	assert.Equal(t, "/static/favicon.ico", redirect.Header().Get("Location"))

	for _, asset := range []string{"swol.svg", "favicon-32.png", "apple-touch-icon.png", "favicon.ico"} {
		resp := httptest.NewRecorder()
		newServer(cfg, nil, nil).routes().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/static/"+asset, nil))
		assert.Equal(t, http.StatusOK, resp.Code, asset)
		assert.Greater(t, resp.Body.Len(), 0, asset)
	}
}

func configBoolptr(v bool) *bool { return &v }

func TestMachineConfigDetailsRedactsSecrets(t *testing.T) {
	machine := config.Machine{
		Name: "test", Mac: "01:02:03:04:05:06", IP: strptr("192.168.1.22"),
		SOLAuthUsername: "user", SOLAuthPassword: "superSecretPassword",
		SOLHeaders:     map[string]string{"Authorization": "Bearer my-token", "X-API-Key": "api-secret", "X-Region": "home"},
		SOLQueryParams: map[string]string{"token": "token-value", "format": "JSON"},
		Broadcast:      &config.Broadcast{Address: "192.168.1.255", Port: 9},
	}
	got := machineConfigDetails(machine, []config.Schedule{{Name: "night", Machine: "test", Cron: "@daily"}})
	assert.NotContains(t, got, "superSecretPassword")
	assert.NotContains(t, got, "my-token")
	assert.NotContains(t, got, "api-secret")
	assert.NotContains(t, got, "token-value")
	assert.Contains(t, got, `"sol_auth_password": "[redacted]"`)
	assert.Contains(t, got, `"X-Region": "home"`)
	assert.Contains(t, got, `"address": "192.168.1.255"`)
	assert.Contains(t, got, `"show_mac": true`)
	assert.Contains(t, got, `"name": "night"`)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(got), &payload))
}

func TestMachineConfigDetailsPrivacyMasking(t *testing.T) {
	m := config.Machine{
		Name: "private", Mac: "01:02:03:04:05:06", IP: strptr("192.168.11.20"),
		Icon: "pc-display", SOLIP: "192.168.11.21", Broadcast: &config.Broadcast{Address: "192.168.11.255", Port: 9},
	}
	displayed := machineConfigDetails(m, nil)
	masked := machineConfigDetailsWithPrivacy(m, nil, true)
	assert.Contains(t, displayed, `"icon": "pc-display"`)
	for _, secret := range []string{"01:02:03:04:05:06", "192.168.11.20", "192.168.11.21", "192.168.11.255"} {
		assert.NotContains(t, masked, secret)
	}
	assert.Contains(t, masked, `"mac": "[masked]"`)
	assert.Contains(t, masked, `"ip": "[masked]"`)
}

func TestHandleIndexShowsSleepButtonOnlyWhenEnabledAndIPConfigured(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{
			{Name: "desktop", Mac: "01:02:03:04:05:06", IP: strptr("192.168.1.105"), SOLEnabled: true},
			{Name: "no-ip", Mac: "0a:0b:0c:0d:0e:0f"},
			{Name: "not-opted-in", Mac: "0b:0c:0d:0e:0f:10", IP: strptr("192.168.1.110")},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Equal(t, 1, strings.Count(body, `class="machine__power-button machine__power-button--sleep"`))
	parts := strings.Split(body, `<li class="machine"`)
	require.Len(t, parts, 4)
	assert.Contains(t, parts[1], `class="machine__power-button machine__power-button--sleep"`)
	assert.NotContains(t, parts[2], `class="machine__power-button machine__power-button--sleep"`)
	assert.NotContains(t, parts[3], `class="machine__power-button machine__power-button--sleep"`)
}

func TestHandleIndexShowsScheduleChip(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{
			{Name: "scheduled", Mac: "01:02:03:04:05:06"},
			{Name: "plain", Mac: "0a:0b:0c:0d:0e:0f"},
		},
		Schedules: []config.Schedule{{Machine: "scheduled", Cron: "0 2 * * 6"}},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// The scheduled machine renders a chip with its cron expression...
	assert.Contains(t, body, "0 2 * * 6")
	// ...and there is exactly one chip (the unscheduled machine has none, and a
	// single schedule produces no "+N" overflow pill). Match the rendered class
	// attribute, not the bare name, which also appears in the <style> block.
	assert.Equal(t, 1, strings.Count(body, `class="machine__schedule"`))
	assert.NotContains(t, body, `class="machine__schedule machine__schedule--more"`)
}

func TestHandleIndexCollapsesExtraSchedules(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{{Name: "media-server", Mac: "01:02:03:04:05:06"}},
		Schedules: []config.Schedule{
			{Machine: "media-server", Cron: "@daily"},
			{Machine: "media-server", Cron: "@every 1m"},
			{Machine: "media-server", Cron: "0 18 * * 1-5"},
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newServer(cfg, nil, nil).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// Only the first schedule is shown as a visible chip; the rest collapse into
	// a "+2" overflow pill (their crons live in its title for hover).
	assert.Equal(t, 1, strings.Count(body, `class="machine__schedule"`))
	assert.Contains(t, body, `class="machine__schedule machine__schedule--more"`)
	assert.Contains(t, body, "@daily")
	assert.Contains(t, body, ">+2<")
}

func TestHandleWakeSuccess(t *testing.T) {
	const macStr = "01:02:03:04:05:06"
	cfg := &config.Config{
		// The machine overrides only the broadcast address, so the port should be
		// inherited from the global default.
		Broadcast: config.Broadcast{Address: "255.255.255.255", Port: 9},
		Machines: []config.Machine{{
			Name:      "alpha",
			Mac:       macStr,
			Broadcast: &config.Broadcast{Address: "192.168.1.255"},
		}},
	}

	var (
		gotMac  net.HardwareAddr
		gotAddr string
	)
	wake := func(mac net.HardwareAddr, addr string) error {
		gotMac = mac
		gotAddr = addr
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/wake", strings.NewReader("name=alpha"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newServer(cfg, nil, wake).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))

	// The configured machine's MAC was parsed and handed to the wake seam.
	want, err := net.ParseMAC(macStr)
	require.NoError(t, err)
	assert.Equal(t, want, gotMac)

	// The per-machine broadcast override was resolved (address overridden, port
	// inherited from the global default) and passed to the wake seam.
	assert.Equal(t, "192.168.1.255:9", gotAddr)

	// A flash message cookie naming the machine is set.
	var flash *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "flash" {
			flash = c
		}
	}
	require.NotNil(t, flash)
	assert.Contains(t, flash.Value, "alpha")
}

// Non-ASCII machine names must survive the flash cookie round-trip (issue #35).
func TestHandleWakeNonASCIIName(t *testing.T) {
	const machineName = "客厅电脑"
	cfg := &config.Config{
		Broadcast: config.Broadcast{Address: "255.255.255.255", Port: 9},
		Machines:  []config.Machine{{Name: machineName, Mac: "01:02:03:04:05:06"}},
	}

	wake := func(mac net.HardwareAddr, addr string) error { return nil }
	body := "name=" + url.QueryEscape(machineName)
	req := httptest.NewRequest(http.MethodPost, "/wake", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newServer(cfg, nil, wake).routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)

	var flash *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "flash" {
			flash = c
		}
	}
	require.NotNil(t, flash)

	// Decoded value must preserve the non-ASCII name (no bytes dropped).
	got, err := url.QueryUnescape(flash.Value)
	require.NoError(t, err)
	assert.Contains(t, got, machineName)
}

func TestHandleWakeMachineNotFound(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{{Name: "alpha", Mac: "01:02:03:04:05:06"}},
	}

	called := false
	wake := func(mac net.HardwareAddr, addr string) error {
		called = true
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/wake", strings.NewReader("name=ghost"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	newServer(cfg, nil, wake).routes().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, called, "wake must not be called for an unknown machine")
}

func TestHandleSleepSuccess(t *testing.T) {
	cfg := &config.Config{
		SOLPort:           8009,
		SOLPath:           "/sleep",
		SOLConnectTimeout: "3s",
		SOLTimeout:        "8s",
		Machines: []config.Machine{{
			Name: "desktop", IP: strptr("192.168.1.105"),
			SOLEnabled: true, SOLIP: "192.168.1.106",
		}},
	}

	var gotRequest config.SOLRequest
	srv := newServer(cfg, nil, nil)
	srv.sleep = func(_ context.Context, request config.SOLRequest) error {
		gotRequest = request
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/sleep", strings.NewReader("name=desktop"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/", rec.Header().Get("Location"))
	assert.Equal(t, "http://192.168.1.106:8009/sleep", gotRequest.Endpoint)
	assert.Equal(t, 3*time.Second, gotRequest.ConnectTimeout)
	assert.Equal(t, 8*time.Second, gotRequest.Timeout)

	var flash *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "flash" {
			flash = cookie
		}
	}
	require.NotNil(t, flash)
	message, err := url.QueryUnescape(flash.Value)
	require.NoError(t, err)
	assert.Contains(t, message, "Sleep request accepted by desktop")
}

func TestHandleSleepRejectsDisabledOrUnconfiguredMachine(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		wantStatus int
	}{
		{
			name:       "disabled",
			cfg:        &config.Config{Machines: []config.Machine{{Name: "desktop"}}},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "machine not opted in",
			cfg: &config.Config{
				Machines: []config.Machine{{Name: "desktop", IP: strptr("192.168.1.105")}},
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "missing IP",
			cfg: &config.Config{
				Machines: []config.Machine{{Name: "desktop", SOLEnabled: true}},
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(tt.cfg, nil, nil)
			sleepCalled := false
			srv.sleep = func(context.Context, config.SOLRequest) error {
				sleepCalled = true
				return nil
			}
			req := httptest.NewRequest(http.MethodPost, "/sleep", strings.NewReader("name=desktop"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.False(t, sleepCalled)
		})
	}
}

func TestRequestSleepCallsGETEndpoint(t *testing.T) {
	called := false
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/sleep", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()

	err := requestSleep(context.Background(), config.SOLRequest{
		Endpoint: endpoint.URL + "/sleep", ConnectTimeout: time.Second, Timeout: 2 * time.Second,
	})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRequestSleepSendsBasicAuthAndCustomHeaders(t *testing.T) {
	called := false
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		username, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "sol-user", username)
		assert.Equal(t, "sol-password", password)
		assert.Equal(t, "proxy-token", r.Header.Get("X-Proxy-Token"))
		assert.Equal(t, "wol-ui", r.URL.Query().Get("source"))
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()

	err := requestSleep(context.Background(), config.SOLRequest{
		Endpoint: endpoint.URL + "/sleep?source=wol-ui", ConnectTimeout: time.Second, Timeout: 2 * time.Second,
		Username: "sol-user", Password: "sol-password", Headers: map[string]string{"X-Proxy-Token": "proxy-token"},
	})
	require.NoError(t, err)
	assert.True(t, called)
}

func TestRequestSleepRejectsNonSuccessStatus(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer endpoint.Close()

	err := requestSleep(context.Background(), config.SOLRequest{
		Endpoint: endpoint.URL, ConnectTimeout: time.Second, Timeout: 2 * time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 403")
}

func TestWriteMachinesStatusFrame(t *testing.T) {
	cfg := &config.Config{
		Machines: []config.Machine{
			{Name: "on", IP: strptr("10.0.0.1")},
			{Name: "off", IP: strptr("10.0.0.2")},
			{Name: "unknown", IP: nil},
		},
	}
	pinger := fakePinger{reachable: map[string]bool{
		"10.0.0.1": true,
		"10.0.0.2": false,
	}}
	var buf bytes.Buffer
	require.NoError(t, newServer(cfg, pinger, nil).writeMachinesStatus(&buf))

	out := buf.String()
	require.True(t, strings.HasPrefix(out, "data: "), "frame must start with an SSE data field")
	require.True(t, strings.HasSuffix(out, "\n\n"), "frame must end with a blank line")

	payload := strings.TrimSuffix(strings.TrimPrefix(out, "data: "), "\n\n")
	var statuses map[string]string
	require.NoError(t, json.Unmarshal([]byte(payload), &statuses))
	assert.Equal(t, map[string]string{
		"on":      "online",
		"off":     "offline",
		"unknown": "unknown",
	}, statuses)
}
