package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/rawbytes"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

const (
	koanfDelimiter = "."
	koanfTag       = "koanf"
	configFilename = "config.yaml"
	configEnvVar   = "WOL_CONFIG"
)

// Machine represents a machine to wake up
type Machine struct {
	// Name of the machine
	Name string `koanf:"name"`
	// MAC address of the machine
	Mac string `koanf:"mac"`
	// Optional Bootstrap Icons glyph name (e.g. laptop, pc-display, hdd-network).
	Icon string `koanf:"icon"`
	// Hostname or IP address of the machine (optional)
	IP *string `koanf:"ip"`
	// Optional UI visibility settings. Nil means visible, so existing configs
	// continue to show all three by default; false explicitly hides a feature.
	ShowMAC     *bool `koanf:"show_mac"`
	ShowIP      *bool `koanf:"show_ip"`
	ShowDetails *bool `koanf:"show_details"`
	// Sleep-On-LAN settings can be overridden independently for each machine.
	SOLEnabled        bool              `koanf:"sol_enabled"`
	SOLIP             string            `koanf:"sol_ip"`
	SOLPort           int               `koanf:"sol_port"`
	SOLProtocol       string            `koanf:"sol_protocol"`
	SOLPath           string            `koanf:"sol_path"`
	SOLFormat         string            `koanf:"sol_format"`
	SOLAuthUsername   string            `koanf:"sol_auth_username"`
	SOLAuthPassword   string            `koanf:"sol_auth_password"`
	SOLConnectTimeout string            `koanf:"sol_connect_timeout"`
	SOLTimeout        string            `koanf:"sol_timeout"`
	SOLHeaders        map[string]string `koanf:"sol_headers"`
	SOLQueryParams    map[string]string `koanf:"sol_query_params"`
	// Broadcast optionally overrides the global broadcast target for this
	// machine. Zero-value fields inherit the global setting. This is what lets a
	// machine on a different subnet/VLAN use its own directed broadcast address.
	Broadcast *Broadcast `koanf:"broadcast"`
}

// bootstrapIconName accepts a single Bootstrap Icons slug, not arbitrary CSS.
var bootstrapIconName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// IconClass returns a safe CSS class for a custom Bootstrap Icons glyph.
// Both `icon: laptop` and `icon: bi-laptop` are accepted. An invalid value
// produces no custom icon rather than inserting arbitrary HTML/CSS classes.
func (m Machine) IconClass() string {
	icon := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(m.Icon)), "bi-")
	if icon == "" || !bootstrapIconName.MatchString(icon) {
		return ""
	}
	return "bi bi-" + icon
}

// MACVisible reports whether the MAC address appears on the machine card.
// The default for an omitted show_mac key is true.
func (m Machine) MACVisible() bool { return m.ShowMAC == nil || *m.ShowMAC }

// IPVisible reports whether the IP address appears on the machine card.
// The default for an omitted show_ip key is true.
func (m Machine) IPVisible() bool { return m.ShowIP == nil || *m.ShowIP }

// DetailsVisible reports whether the machine name opens its configuration.
// The default for an omitted show_details key is true.
func (m Machine) DetailsVisible() bool { return m.ShowDetails == nil || *m.ShowDetails }

// HardwareAddr parses and returns the machine's MAC address.
func (m Machine) HardwareAddr() (net.HardwareAddr, error) {
	mac, err := net.ParseMAC(m.Mac)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MAC address: %w", err)
	}
	return mac, nil
}

// Broadcast is the destination a magic packet is sent to. It is reused for the
// global default and for per-machine overrides; on an override, a zero-value
// field (empty Address or zero Port) inherits the global value.
type Broadcast struct {
	// Address is the broadcast address to send the magic packet to. Use the
	// subnet-directed broadcast (e.g. 192.168.1.255) to reliably reach a device
	// on a specific subnet from a multi-homed host, rather than the limited
	// broadcast 255.255.255.255.
	Address string `koanf:"address"`
	// Port is the UDP port to send the magic packet to (typically 9, sometimes 7).
	Port int `koanf:"port"`
}

// Addr returns the host:port string the magic packet is dialed to,
// e.g. "192.168.1.255:9".
func (b Broadcast) Addr() string {
	return net.JoinHostPort(b.Address, strconv.Itoa(b.Port))
}

// Server represents the server configuration
type Server struct {
	// Listen address for the server
	Listen string `koanf:"listen"`
}

// Ping represents the ping configuration
type Ping struct {
	// Privileged determines if privileged ping should be used
	Privileged bool `koanf:"privileged"`
}

// Sleep contains legacy fallback values for SOL request settings. New configs
// should use the sol_* keys; enablement is controlled only by each machine's
// sol_enabled flag.
type Sleep struct {
	Port           int    `koanf:"port"`
	Path           string `koanf:"path"`
	ConnectTimeout string `koanf:"connect_timeout"`
	Timeout        string `koanf:"timeout"`
}

// SOLRequest contains the resolved HTTP request settings for one machine.
// The Sleep-On-LAN service exposes GET routes; the method is intentionally fixed.
type SOLRequest struct {
	Endpoint       string
	ConnectTimeout time.Duration
	Timeout        time.Duration
	Username       string
	Password       string
	Headers        map[string]string
}

// SOLRequestFor resolves global sol_* defaults and per-machine sol_* overrides.
// The machine's sol_enabled flag is the only enable switch.
func (c *Config) SOLRequestFor(machine Machine) (SOLRequest, error) {
	if !machine.SOLEnabled {
		return SOLRequest{}, fmt.Errorf("Sleep-On-LAN is not enabled for machine %q", machine.Name)
	}

	host := strings.TrimSpace(firstNonEmpty(machine.SOLIP, c.SOLIP))
	if host == "" && machine.IP != nil {
		host = strings.TrimSpace(*machine.IP)
	}
	if host == "" {
		return SOLRequest{}, fmt.Errorf("machine %q has no Sleep-On-LAN IP configured (set sol_ip or ip)", machine.Name)
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if strings.ContainsAny(host, "/?#@ \t\r\n") {
		return SOLRequest{}, fmt.Errorf("machine %q has an invalid sol_ip hostname or address", machine.Name)
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return SOLRequest{}, fmt.Errorf("machine %q: set the host only in sol_ip and configure the port separately with sol_port", machine.Name)
	}

	port := firstNonZero(machine.SOLPort, c.SOLPort, c.Sleep.Port, 8009)
	if port < 1 || port > 65535 {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_port must be between 1 and 65535", machine.Name)
	}

	protocol := strings.ToLower(strings.TrimSpace(firstNonEmpty(machine.SOLProtocol, c.SOLProtocol, "http")))
	if protocol != "http" && protocol != "https" {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_protocol must be http or https", machine.Name)
	}

	path := strings.TrimSpace(firstNonEmpty(machine.SOLPath, c.SOLPath, c.Sleep.Path, "/sleep"))
	parsedPath, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || parsedPath.IsAbs() || parsedPath.Host != "" || parsedPath.RawQuery != "" || parsedPath.Fragment != "" {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_path must be an absolute URL path without a query or fragment", machine.Name)
	}

	format := strings.ToUpper(strings.TrimSpace(firstNonEmpty(machine.SOLFormat, c.SOLFormat)))
	if format != "" && format != "JSON" {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_format must be JSON or empty (use the service default)", machine.Name)
	}

	username := firstNonEmpty(machine.SOLAuthUsername, c.SOLAuthUsername)
	password := firstNonEmpty(machine.SOLAuthPassword, c.SOLAuthPassword)
	if (username == "") != (password == "") {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_auth_username and sol_auth_password must be set together", machine.Name)
	}

	connectTimeoutValue := strings.TrimSpace(firstNonEmpty(machine.SOLConnectTimeout, c.SOLConnectTimeout, c.Sleep.ConnectTimeout, "3s"))
	connectTimeout, err := time.ParseDuration(connectTimeoutValue)
	if err != nil || connectTimeout <= 0 {
		return SOLRequest{}, fmt.Errorf("machine %q: invalid sol_connect_timeout %q", machine.Name, connectTimeoutValue)
	}
	timeoutValue := strings.TrimSpace(firstNonEmpty(machine.SOLTimeout, c.SOLTimeout, c.Sleep.Timeout, "8s"))
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil || timeout <= 0 {
		return SOLRequest{}, fmt.Errorf("machine %q: invalid sol_timeout %q", machine.Name, timeoutValue)
	}
	if timeout < connectTimeout {
		return SOLRequest{}, fmt.Errorf("machine %q: sol_timeout must be greater than or equal to sol_connect_timeout", machine.Name)
	}

	headers := mergeHeaderMaps(c.SOLHeaders, machine.SOLHeaders)
	for name, value := range headers {
		if !validHTTPHeaderName(name) || strings.ContainsAny(value, "\r\n") {
			return SOLRequest{}, fmt.Errorf("machine %q: invalid sol_headers entry %q", machine.Name, name)
		}
	}

	queryParams := mergeStringMaps(c.SOLQueryParams, machine.SOLQueryParams)
	for name := range queryParams {
		if strings.TrimSpace(name) == "" {
			return SOLRequest{}, fmt.Errorf("machine %q: sol_query_params cannot contain an empty key", machine.Name)
		}
	}
	if format != "" {
		queryParams["format"] = format
	}

	endpointURL := url.URL{
		Scheme: protocol,
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   path,
	}
	query := endpointURL.Query()
	for name, value := range queryParams {
		query.Set(name, value)
	}
	endpointURL.RawQuery = query.Encode()

	return SOLRequest{
		Endpoint:       endpointURL.String(),
		ConnectTimeout: connectTimeout,
		Timeout:        timeout,
		Username:       username,
		Password:       password,
		Headers:        headers,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		merged[key] = value
	}
	return merged
}

func mergeHeaderMaps(base, override map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(override))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		for existing := range merged {
			if strings.EqualFold(existing, key) {
				delete(merged, existing)
			}
		}
		merged[key] = value
	}
	return merged
}

func validHTTPHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			continue
		}
		return false
	}
	return true
}

// Schedule represents an automatic cron wake-up for a configured machine.
type Schedule struct {
	// Name is an optional label used in log output. When empty the machine
	// name is used instead.
	Name string `koanf:"name"`
	// Machine is the name of a configured machine to wake (required).
	Machine string `koanf:"machine"`
	// Cron is the schedule expression, e.g. "0 2 * * 6" (required). It is
	// evaluated in the server's local time and supports the standard five
	// fields as well as descriptors like @daily and @every.
	Cron string `koanf:"cron"`
}

// Config represents the configuration for the application
type Config struct {
	// Machines represents the list of machines to wake up
	Machines []Machine `koanf:"machines"`
	// Server represents the server configuration
	Server Server `koanf:"server"`
	// Ping represents the ping configuration
	Ping Ping `koanf:"ping"`
	// Sleep contains legacy fallback values for SOL request settings.
	Sleep Sleep `koanf:"sleep"`
	// Global sol_* request defaults. Enablement is always per machine via sol_enabled.
	SOLIP             string            `koanf:"sol_ip"`
	SOLPort           int               `koanf:"sol_port"`
	SOLProtocol       string            `koanf:"sol_protocol"`
	SOLPath           string            `koanf:"sol_path"`
	SOLFormat         string            `koanf:"sol_format"`
	SOLAuthUsername   string            `koanf:"sol_auth_username"`
	SOLAuthPassword   string            `koanf:"sol_auth_password"`
	SOLConnectTimeout string            `koanf:"sol_connect_timeout"`
	SOLTimeout        string            `koanf:"sol_timeout"`
	SOLHeaders        map[string]string `koanf:"sol_headers"`
	SOLQueryParams    map[string]string `koanf:"sol_query_params"`
	// Broadcast represents the default broadcast target for magic packets.
	Broadcast Broadcast `koanf:"broadcast"`
	// Schedules represents optional cron wake-ups run by the serve command.
	// When empty, scheduling is disabled.
	Schedules []Schedule `koanf:"schedules"`

	// sources records which inputs contributed to the loaded config, in load
	// order. It is unexported so koanf's reflection-based providers ignore it.
	sources []string
}

// NewConfig creates a new Config instance
func NewConfig() *Config {
	return &Config{}
}

// Sources returns the inputs that contributed to the last Load, in load order
// (e.g. config file paths and the WOL_CONFIG environment variable).
func (c *Config) Sources() []string {
	return c.sources
}

// MachineByName returns the configured machine with the given name
// (case-insensitive).
func (c *Config) MachineByName(name string) (Machine, error) {
	for _, m := range c.Machines {
		if strings.EqualFold(m.Name, name) {
			return m, nil
		}
	}

	return Machine{}, fmt.Errorf("machine with name %q not found", name)
}

// BroadcastFor returns the effective broadcast target for a machine: the global
// broadcast setting with any non-zero per-machine override fields applied on top.
func (c *Config) BroadcastFor(m Machine) Broadcast {
	b := c.Broadcast
	if m.Broadcast != nil {
		if m.Broadcast.Address != "" {
			b.Address = m.Broadcast.Address
		}
		if m.Broadcast.Port != 0 {
			b.Port = m.Broadcast.Port
		}
	}
	return b
}

// defaultConfigPaths lists configuration files in ascending priority.
// SWOL does not automatically load config files from older WOL directories.
func defaultConfigPaths(home string) []string {
	return []string{
		filepath.Join("/etc", "swol", configFilename),
		filepath.Join(home, ".swol", configFilename),
		filepath.Join(".", configFilename),
	}
}

// Load loads the configuration. The contributing sources are recorded and can be
// retrieved with Sources (in load order, later values overriding earlier ones).
//
// If path is non-empty it is used as the sole config file and must exist (a
// missing file is an error). An explicit --config file is authoritative: the
// default search locations and the WOL_CONFIG environment variable are ignored.
//
// If path is empty, the default locations are searched and any that are missing
// are skipped, with WOL_CONFIG layered on top as the highest-precedence source:
//   - /etc/swol/config.yaml
//   - ~/.swol/config.yaml
//   - ./config.yaml
//
// In all cases the built-in defaults are the lowest-priority layer.
func (c *Config) Load(path string) error {
	// An explicit config file replaces the default search locations and must exist.
	paths := []string{path}
	explicit := true

	if path == "" {
		explicit = false
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		// Order here matters as later values will override earlier ones.
		paths = defaultConfigPaths(home)
	}

	sources, err := c.load(paths, explicit)
	if err != nil {
		return err
	}
	c.sources = sources
	return nil
}

// load reads configuration into c from the given file paths (in precedence order,
// later overriding earlier) layered on top of the defaults. When explicit is
// false it then layers the WOL_CONFIG environment variable on top. It returns the
// sources that actually contributed, in load order, so callers can report which
// configuration was used.
//
// explicit selects the behavior for an explicit --config file: the file must
// exist (a missing file is an error) and it is used on its own - WOL_CONFIG is
// ignored. With auto-discovery (explicit false) missing files are skipped and
// WOL_CONFIG is layered on top.
//
// It uses a fresh koanf instance per call so repeated loads don't accumulate
// state, and takes its search paths as an argument so tests can point it at
// temporary files instead of the real /etc/swol or ~/.swol.
func (c *Config) load(paths []string, explicit bool) ([]string, error) {
	k := koanf.New(koanfDelimiter)

	// Defaults are the lowest-priority layer.
	defaults := &Config{
		Server: Server{
			Listen: ":7777",
		},
		Ping: Ping{
			Privileged: false,
		},
		Sleep: Sleep{
			Port:           8009,
			Path:           "/sleep",
			ConnectTimeout: "3s",
			Timeout:        "8s",
		},
		Broadcast: Broadcast{
			Address: "255.255.255.255",
			Port:    9,
		},
	}
	if err := k.Load(structs.Provider(defaults, koanfTag), nil); err != nil {
		return nil, fmt.Errorf("failed to load defaults: %w", err)
	}

	var sources []string
	for _, path := range paths {
		err := k.Load(file.Provider(path), yaml.Parser())
		if err != nil {
			if os.IsNotExist(err) {
				// A missing explicit file is an error; a missing search-path
				// location is simply skipped.
				if explicit {
					return nil, fmt.Errorf("config file %q does not exist", path)
				}
				continue
			}
			return nil, fmt.Errorf("failed to load config file %q: %w", path, err)
		}
		sources = append(sources, path)
	}

	// WOL_CONFIG overrides the discovered files; an explicit --config file is
	// authoritative, so the env var is ignored in that mode.
	if ec := os.Getenv(configEnvVar); !explicit && ec != "" {
		if err := k.Load(rawbytes.Provider([]byte(ec)), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("failed to load config from %s: %w", configEnvVar, err)
		}
		sources = append(sources, configEnvVar+" environment variable")
	}

	if err := k.Unmarshal("", c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return sources, nil
}
