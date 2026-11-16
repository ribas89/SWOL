package cmd

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/spf13/cobra"
	"swol/config"
	"swol/magicpacket"
)

//go:embed templates/*
var templates embed.FS

//go:embed static
var staticFiles embed.FS

func init() {
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve a web interface to manage machine power",
	Long:  "Serve a web interface that lists configured machines and allows Wake-on-LAN and optional Sleep-On-LAN actions",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		if sources := cfg.Sources(); len(sources) > 0 {
			log.Printf("Loaded config from %s", strings.Join(sources, ", "))
		} else {
			log.Print("No config file found; using built-in defaults")
		}

		// Start cron wake-ups when configured; a misconfigured schedule
		// (unknown machine or invalid cron) aborts serve before it listens.
		if len(cfg.Schedules) > 0 {
			sched, err := newScheduler(cfg, broadcastWake)
			cobra.CheckErr(err)
			sched.start()
			log.Printf("Started %d scheduled wake-up(s)", len(cfg.Schedules))
		}

		enabledMachines := 0
		for _, machine := range cfg.Machines {
			if !machine.SOLEnabled {
				continue
			}
			request, err := cfg.SOLRequestFor(machine)
			if err != nil {
				cobra.CheckErr(err)
			}
			enabledMachines++
			log.Printf("Sleep-On-LAN enabled for %s at %s", machine.Name, request.Endpoint)
		}
		if enabledMachines > 0 {
			log.Printf("Sleep-On-LAN enabled for %d machine(s)", enabledMachines)
		}

		handler := newServer(cfg, newProbingPinger(cfg.Ping.Privileged), broadcastWake).routes()

		log.Printf("Listening on %s", cfg.Server.Listen)
		err := http.ListenAndServe(cfg.Server.Listen, handler)
		if err != nil {
			cobra.CheckErr(err)
		}
	},
}

// Pinger reports whether a network address is currently reachable.
type Pinger interface {
	Reachable(addr string) (bool, error)
}

// waker sends a wake-on-LAN magic packet to the given MAC address, broadcasting
// to addr (a "host:port" target).
type waker func(mac net.HardwareAddr, addr string) error

// sleepRequester sends an HTTP request using the resolved Sleep-On-LAN options.
type sleepRequester func(ctx context.Context, request config.SOLRequest) error

// broadcastWake is the production waker: it broadcasts a real magic packet.
func broadcastWake(mac net.HardwareAddr, addr string) error {
	return magicpacket.NewMagicPacket(mac).Broadcast(addr)
}

// server holds the dependencies for the web handlers. Injecting them (rather
// than reaching for package globals) lets tests substitute fakes for the pinger
// and wake action, so handlers can be exercised without real ICMP or UDP traffic.
type server struct {
	cfg    *config.Config
	pinger Pinger
	wake   waker
	sleep  sleepRequester
}

func newServer(cfg *config.Config, pinger Pinger, wake waker) *server {
	return &server{cfg: cfg, pinger: pinger, wake: wake, sleep: requestSleep}
}

// routes registers all HTTP routes and returns the handler.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("POST /wake", s.handleWake)
	mux.HandleFunc("POST /sleep", s.handleSleep)
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.ico", http.StatusPermanentRedirect)
	})

	// Serve embedded static assets (favicon, logo) under /static/. The embed
	// keeps them under a "static/" prefix that matches the URL prefix, so the
	// file server maps requests straight to the embedded paths.
	mux.Handle("GET /static/", http.FileServerFS(staticFiles))

	return mux
}

// machineView keeps web-only data separate from the configuration used by the
// wake/sleep endpoints. Machine is embedded so the template can use its usual
// fields; DetailsJSON contains a redacted representation of its configuration.
type machineView struct {
	config.Machine
	DetailsJSON       string
	DetailsJSONMasked string
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// Parse the template
	index, err := template.ParseFS(templates, "templates/index.html")
	if err != nil {
		log.Printf("Error parsing template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Group schedules by their resolved machine name so the template can show a
	// per-machine indicator. Keyed by the canonical machine name (what the card
	// renders) so lookup matches regardless of how the schedule spelled it.
	schedulesByMachine := make(map[string][]config.Schedule)
	for _, sch := range s.cfg.Schedules {
		m, err := s.cfg.MachineByName(sch.Machine)
		if err != nil {
			continue // unknown machine; already rejected at serve startup
		}
		schedulesByMachine[m.Name] = append(schedulesByMachine[m.Name], sch)
	}

	// Prepare an explicitly redacted copy for the details panel. Never send
	// configured HTTP Basic Auth passwords or API tokens to the browser.
	machineViews := make([]machineView, 0, len(s.cfg.Machines))
	for _, machine := range s.cfg.Machines {
		view := machineView{Machine: machine}
		if machine.DetailsVisible() {
			view.DetailsJSON = machineConfigDetails(machine, schedulesByMachine[machine.Name])
			view.DetailsJSONMasked = machineConfigDetailsWithPrivacy(machine, schedulesByMachine[machine.Name], true)
		}
		machineViews = append(machineViews, view)
	}

	// Execute the template
	data := map[string]interface{}{
		"Machines":           machineViews,
		"SOLIP":              s.cfg.SOLIP,
		"SchedulesByMachine": schedulesByMachine,
		"FlashMessage":       consumeFlashMessage(w, r), // Get flash message from cookie
	}
	err = index.Execute(w, data)
	if err != nil {
		log.Printf("Error executing template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

// setFlashMessage stores a flash message in a cookie, URL-encoded so non-ASCII
// bytes survive net/http's cookie-value validation rather than being dropped.
func setFlashMessage(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:  "flash",
		Value: url.QueryEscape(message),
		Path:  "/",
	})
}

// consumeFlashMessage retrieves, decodes, and clears the flash message cookie.
func consumeFlashMessage(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie("flash")
	if err == nil {
		// Clear the cookie
		http.SetCookie(w, &http.Cookie{
			Name:    "flash",
			Value:   "",
			Path:    "/",
			Expires: time.Now().Add(-1 * time.Hour),
		})

		// Fall back to the raw value if decoding fails.
		if message, err := url.QueryUnescape(cookie.Value); err == nil {
			return message
		}
		return cookie.Value
	}
	return ""
}

func (s *server) handleWake(w http.ResponseWriter, r *http.Request) {
	machineName := r.FormValue("name")
	machine, err := s.cfg.MachineByName(machineName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mac, err := machine.HardwareAddr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	addr := s.cfg.BroadcastFor(machine).Addr()
	log.Printf("Sending magic packet to %s via %s", mac, addr)
	if err := s.wake(mac, addr); err != nil {
		log.Printf("Error sending magic packet: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set flash message cookie
	setFlashMessage(w, fmt.Sprintf("Wake-up signal sent to %s. The machine should wake up shortly.", machineName))

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleSleep asks the configured Sleep-On-LAN service on a machine to put it
// into sleep mode. The remote endpoint is a GET request; this application only
// exposes the action as POST to avoid triggering it from a link or prefetch.
func (s *server) handleSleep(w http.ResponseWriter, r *http.Request) {
	machineName := r.FormValue("name")
	machine, err := s.cfg.MachineByName(machineName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	request, err := s.cfg.SOLRequestFor(machine)
	if err != nil {
		status := http.StatusBadRequest
		if !machine.SOLEnabled {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	log.Printf("Sending Sleep-On-LAN request to %s at %s", machine.Name, request.Endpoint)
	if err := s.sleep(r.Context(), request); err != nil {
		log.Printf("Error sending Sleep-On-LAN request to %s: %v", machine.Name, err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	setFlashMessage(w, fmt.Sprintf("Sleep request accepted by %s.", machine.Name))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// requestSleep calls the Sleep-On-LAN HTTP endpoint. It deliberately does not
// follow redirects, because a sleep command should only be sent to the configured
// machine and not to a URL supplied by a remote redirect.
func requestSleep(ctx context.Context, options config.SOLRequest) error {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: options.ConnectTimeout}).DialContext,
		TLSHandshakeTimeout: options.ConnectTimeout,
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{
		Transport: transport,
		Timeout:   options.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, options.Endpoint, nil)
	if err != nil {
		return fmt.Errorf("create Sleep-On-LAN request: %w", err)
	}
	for name, value := range options.Headers {
		request.Header.Set(name, value)
	}
	if options.Username != "" || options.Password != "" {
		request.SetBasicAuth(options.Username, options.Password)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("contact Sleep-On-LAN endpoint: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("Sleep-On-LAN endpoint returned HTTP %s", response.Status)
	}
	return nil
}

// machineStatus returns the status of a single machine.
func (s *server) machineStatus(machine config.Machine) (string, error) {
	if machine.IP == nil {
		return "unknown", nil
	}

	reachable, err := s.pinger.Reachable(*machine.IP)
	if err != nil {
		// Don't log here; machinesStatus logs the error once per machine.
		return "unknown", err
	}
	if reachable {
		return "online", nil
	}

	return "offline", nil
}

// machinesStatus returns a map of machine names to their statuses concurrently.
func (s *server) machinesStatus() map[string]string {
	var mu sync.Mutex
	statuses := make(map[string]string)
	var wg sync.WaitGroup

	for _, machine := range s.cfg.Machines {
		wg.Add(1)
		go func(machine config.Machine) {
			defer wg.Done()
			status, err := s.machineStatus(machine)
			if err != nil {
				log.Printf("Error getting status for machine %s: %v", machine.Name, err)
				return
			}

			mu.Lock()
			statuses[machine.Name] = status
			mu.Unlock()
		}(machine)
	}

	wg.Wait()

	return statuses
}

// writeMachinesStatus writes a single SSE frame with the current status of all
// machines to w, flushing if w supports it. Extracted from the streaming loop so
// the frame can be tested without running the loop.
func (s *server) writeMachinesStatus(w io.Writer) error {
	statuses := s.machinesStatus()
	data, err := json.Marshal(statuses)
	if err != nil {
		return fmt.Errorf("error marshaling status: %w", err)
	}

	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return fmt.Errorf("error writing status: %w", err)
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	return nil
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func() {
		if err := s.writeMachinesStatus(w); err != nil {
			log.Print(err)
		}
	}

	// Sends initial status
	send()

	// Send status updates every few seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// probingPinger is the production Pinger, backed by pro-bing.
type probingPinger struct {
	privileged bool
}

func newProbingPinger(privileged bool) *probingPinger {
	return &probingPinger{privileged: privileged}
}

func (p *probingPinger) Reachable(addr string) (bool, error) {
	pinger, err := probing.NewPinger(addr)
	if err != nil {
		// addr couldn't be resolved (e.g. an off machine's hostname). Treat as
		// unreachable instead of erroring, so the status poll doesn't log it each tick.
		return false, nil
	}
	// Set privileged mode based on config
	pinger.SetPrivileged(p.privileged)

	// We only want to ping once and wait 2 seconds for a response
	pinger.Timeout = 2 * time.Second
	pinger.Count = 1

	err = pinger.Run()
	if err != nil {
		return false, fmt.Errorf("error pinging: %w", err)
	}

	// If we receive even a single packet, the address is reachable
	return pinger.Statistics().PacketsRecv > 0, nil
}
