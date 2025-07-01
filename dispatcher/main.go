package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	socket "halox-net-dispatcher/socket"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/coreos/go-systemd/v22/daemon"
	"github.com/godbus/dbus/v5"
)

const (
	DefaultScriptDir = "/etc/networkd-dispatcher:/usr/lib/networkd-dispatcher"
	LogFormat        = "networkd-dispatcher: %s"
)

var (
	singletons = map[string]bool{"Type": true, "ESSID": true, "OperationalState": true}
)

type InterfaceState struct {
	Index          int
	Name           string
	Type           string
	Operational    string
	Administrative string
}

type AddressList struct {
	IPv4 []string
	IPv6 []string
}

type InterfaceData struct {
	Type                string                 `json:"Type"`
	OperationalState    string                 `json:"OperationalState"`
	AdministrativeState string                 `json:"AdministrativeState"`
	InterfaceName       string                 `json:"InterfaceName"`
	State               string                 `json:"State"`
	ESSID               string                 `json:"ESSID,omitempty"`
	Address             []string               `json:"Address,omitempty"`
	Gateway             []string               `json:"Gateway,omitempty"`
	DNS                 []string               `json:"DNS,omitempty"`
	Domain              []string               `json:"Domain,omitempty"`
	Additional          map[string]interface{} `json:",omitempty"`
}

type Dispatcher struct {
	scriptDir         string
	interfacesByIdx   map[int]string
	interfacesByName  map[string]*InterfaceState
	previousIfaceData map[string]*InterfaceData
	conn              *dbus.Conn
	verbose           bool
	socketServer      *socket.SocketServer
}

func NewDispatcher(scriptDir string, socketPath string, verbose bool) *Dispatcher {
	return &Dispatcher{
		scriptDir:         scriptDir,
		interfacesByIdx:   make(map[int]string),
		interfacesByName:  make(map[string]*InterfaceState),
		previousIfaceData: make(map[string]*InterfaceData),
		verbose:           verbose,
		socketServer:      socket.NewSocketServer(socketPath, verbose),
	}
}

func (d *Dispatcher) logf(format string, args ...interface{}) {
	if d.verbose {
		log.Printf(LogFormat, fmt.Sprintf(format, args...))
	}
}

func (d *Dispatcher) errorf(format string, args ...interface{}) {
	log.Printf("ERROR: "+LogFormat, fmt.Sprintf(format, args...))
}

func (d *Dispatcher) scanInterfaces() error {
	cmd := exec.Command("networkctl", "list", "--no-pager", "--no-legend")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("networkctl list failed: %v", err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		name := fields[1]
		ifType := fields[2]
		operational := fields[3]
		administrative := ""
		if len(fields) > 4 {
			administrative = fields[4]
		}

		iface := &InterfaceState{
			Index:          idx,
			Name:           name,
			Type:           ifType,
			Operational:    operational,
			Administrative: administrative,
		}

		// Check if this is a new interface
		if _, exists := d.interfacesByIdx[idx]; !exists {
			d.socketServer.BroadcastInterfaceAdded(name, idx, ifType)
		}

		d.interfacesByIdx[idx] = name
		d.interfacesByName[name] = iface
	}

	d.logf("Interface scan complete, found %d interfaces", len(d.interfacesByName))
	return nil
}

func (d *Dispatcher) getNetworkctlStatus(ifaceName string) (map[string]interface{}, error) {
	cmd := exec.Command("networkctl", "status", "--no-pager", "--no-legend", "--lines=0", "--", ifaceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("networkctl status failed for %s: %v", ifaceName, err)
	}

	data := make(map[string]interface{})
	lines := strings.Split(string(output), "\n")
	var currentKey string

	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}

		parts := strings.SplitN(line, ": ", 2)
		var key, value string

		if len(parts) == 2 {
			key = strings.TrimSpace(parts[0])
			value = strings.TrimSpace(parts[1])
			currentKey = key
		} else if len(parts) == 1 && currentKey != "" {
			key = currentKey
			value = strings.TrimSpace(parts[0])
		} else {
			continue
		}

		if value == "" {
			continue
		}

		// Normalize Address field
		if key == "Address" {
			re := regexp.MustCompile(` \(DHCP4.*\)$`)
			value = re.ReplaceAllString(value, "")
		}

		if singletons[key] {
			data[key] = value
		} else {
			if existing, ok := data[key]; ok {
				if slice, ok := existing.([]string); ok {
					data[key] = append(slice, value)
				} else {
					data[key] = []string{existing.(string), value}
				}
			} else {
				data[key] = []string{value}
			}
		}
	}

	return data, nil
}

func (d *Dispatcher) getWlanESSID(ifaceName string) string {
	// Try iw first, then iwconfig
	if cmd := exec.Command("iw", ifaceName, "link"); cmd.Path != "" {
		if output, err := cmd.Output(); err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.Contains(line, "SSID") {
					parts := strings.Fields(line)
					if len(parts) >= 2 {
						return parts[len(parts)-1]
					}
				}
			}
		}
	}

	if cmd := exec.Command("iwconfig", "--", ifaceName); cmd.Path != "" {
		if output, err := cmd.Output(); err == nil {
			line := strings.Split(string(output), "\n")[0]
			if idx := strings.Index(line, "ESSID:"); idx != -1 {
				essid := line[idx+6:]
				if len(essid) > 2 && essid[0] == '"' && essid[len(essid)-1] == '"' {
					return essid[1 : len(essid)-1]
				}
			}
		}
	}

	return ""
}

func (d *Dispatcher) parseAddresses(addrs []string) AddressList {
	var ipv4, ipv6 []string

	for _, addr := range addrs {
		if strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "fe80:") {
			continue
		}
		if strings.Contains(addr, ":") {
			ipv6 = append(ipv6, addr)
		} else if strings.Contains(addr, ".") {
			ipv4 = append(ipv4, addr)
		}
	}

	return AddressList{IPv4: ipv4, IPv6: ipv6}
}

func (d *Dispatcher) getInterfaceData(iface *InterfaceState) (*InterfaceData, error) {
	data := &InterfaceData{
		Type:                iface.Type,
		OperationalState:    iface.Operational,
		AdministrativeState: iface.Administrative,
		InterfaceName:       iface.Name,
		State:               fmt.Sprintf("%s (%s)", iface.Operational, iface.Administrative),
	}

	// Get detailed status
	if status, err := d.getNetworkctlStatus(iface.Name); err == nil {
		if addr, ok := status["Address"].([]string); ok {
			data.Address = addr
		}
		if gw, ok := status["Gateway"].([]string); ok {
			data.Gateway = gw
		}
		if dns, ok := status["DNS"].([]string); ok {
			data.DNS = dns
		}
		if domain, ok := status["Domain"].([]string); ok {
			data.Domain = domain
		}

		// Update operational state if available in status
		if opState, ok := status["OperationalState"].(string); ok {
			data.OperationalState = opState
		}
	}

	// Get ESSID for wireless interfaces
	if data.Type == "wlan" {
		data.ESSID = d.getWlanESSID(iface.Name)
	}

	return data, nil
}

func (d *Dispatcher) getScriptsForState(state string) ([]string, error) {
	var scripts []string
	seen := make(map[string]bool)

	for _, dir := range strings.Split(d.scriptDir, ":") {
		stateDir := filepath.Join(dir, state+".d")

		entries, err := os.ReadDir(stateDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if seen[name] {
				continue
			}

			fullPath := filepath.Join(stateDir, name)
			if info, err := entry.Info(); err == nil {
				if info.Mode()&0111 != 0 { // executable
					scripts = append(scripts, fullPath)
					seen[name] = true
				}
			}
		}
	}

	sort.Strings(scripts)
	return scripts, nil
}

func (d *Dispatcher) runHooksForState(iface *InterfaceState, state string) error {
	scripts, err := d.getScriptsForState(state)
	if err != nil {
		return err
	}

	if len(scripts) == 0 {
		d.logf("No scripts found for state %s", state)
		return nil
	}

	data, err := d.getInterfaceData(iface)
	if err != nil {
		return fmt.Errorf("failed to get interface data: %v", err)
	}

	addrs := d.parseAddresses(data.Address)
	jsonData, _ := json.Marshal(data)

	env := os.Environ()
	env = append(env,
		fmt.Sprintf("ADDR=%s", func() string {
			if len(data.Address) > 0 {
				return data.Address[0]
			}
			return ""
		}()),
		fmt.Sprintf("ESSID=%s", data.ESSID),
		fmt.Sprintf("IP_ADDRS=%s", strings.Join(addrs.IPv4, " ")),
		fmt.Sprintf("IP6_ADDRS=%s", strings.Join(addrs.IPv6, " ")),
		fmt.Sprintf("IFACE=%s", iface.Name),
		fmt.Sprintf("STATE=%s", state),
		fmt.Sprintf("AdministrativeState=%s", data.AdministrativeState),
		fmt.Sprintf("OperationalState=%s", data.OperationalState),
		fmt.Sprintf("json=%s", string(jsonData)),
	)

	for _, script := range scripts {
		d.logf("Running script %s for interface %s", script, iface.Name)
		cmd := exec.Command(script)
		cmd.Env = env

		if err := cmd.Run(); err != nil {
			d.errorf("Script %s failed: %v", script, err)
		}
	}

	return nil
}

// Helper function to compare address slices
func (d *Dispatcher) compareAddresses(oldAddrs, newAddrs []string) (added, removed []string) {
	oldMap := make(map[string]bool)
	newMap := make(map[string]bool)

	for _, addr := range oldAddrs {
		oldMap[addr] = true
	}

	for _, addr := range newAddrs {
		newMap[addr] = true
		if !oldMap[addr] {
			added = append(added, addr)
		}
	}

	for _, addr := range oldAddrs {
		if !newMap[addr] {
			removed = append(removed, addr)
		}
	}

	return added, removed
}

// Check for IP address changes and broadcast events
func (d *Dispatcher) checkAddressChanges(ifaceName string, currentData *InterfaceData) {
	iface := d.interfacesByName[ifaceName]
	if iface == nil {
		return
	}

	previousData, exists := d.previousIfaceData[ifaceName]
	if !exists {
		// First time seeing this interface, just store the data
		d.previousIfaceData[ifaceName] = currentData
		return
	}

	// Compare addresses
	oldAddrs := previousData.Address
	newAddrs := currentData.Address

	added, removed := d.compareAddresses(oldAddrs, newAddrs)

	// Broadcast IP address added events
	for _, addr := range added {
		d.logf("IP address added on interface %s: %s", ifaceName, addr)
		d.socketServer.BroadcastIPAddressAdded(ifaceName, iface.Index, addr, iface.Type)

		// Run hooks for ip-added state
		if err := d.runHooksForIPEvent(iface, "ip-added", addr, currentData); err != nil {
			d.errorf("Failed to run ip-added hooks for %s: %v", ifaceName, err)
		}
	}

	// Broadcast IP address removed events
	for _, addr := range removed {
		d.logf("IP address removed from interface %s: %s", ifaceName, addr)
		d.socketServer.BroadcastIPAddressRemoved(ifaceName, iface.Index, addr, iface.Type)

		// Run hooks for ip-removed state
		if err := d.runHooksForIPEvent(iface, "ip-removed", addr, currentData); err != nil {
			d.errorf("Failed to run ip-removed hooks for %s: %v", ifaceName, err)
		}
	}

	// Update stored data
	d.previousIfaceData[ifaceName] = currentData
}

// Run hooks for IP address events
func (d *Dispatcher) runHooksForIPEvent(iface *InterfaceState, eventType, address string, data *InterfaceData) error {
	scripts, err := d.getScriptsForState(eventType)
	if err != nil {
		return err
	}

	if len(scripts) == 0 {
		d.logf("No scripts found for state %s", eventType)
		return nil
	}

	addrs := d.parseAddresses(data.Address)
	jsonData, _ := json.Marshal(data)

	env := os.Environ()
	env = append(env,
		fmt.Sprintf("ADDR=%s", address), // The specific address that changed
		fmt.Sprintf("ESSID=%s", data.ESSID),
		fmt.Sprintf("IP_ADDRS=%s", strings.Join(addrs.IPv4, " ")),
		fmt.Sprintf("IP6_ADDRS=%s", strings.Join(addrs.IPv6, " ")),
		fmt.Sprintf("IFACE=%s", iface.Name),
		fmt.Sprintf("STATE=%s", eventType),
		fmt.Sprintf("AdministrativeState=%s", data.AdministrativeState),
		fmt.Sprintf("OperationalState=%s", data.OperationalState),
		fmt.Sprintf("json=%s", string(jsonData)),
	)

	for _, script := range scripts {
		d.logf("Running script %s for interface %s (%s: %s)", script, iface.Name, eventType, address)
		cmd := exec.Command(script)
		cmd.Env = env

		if err := cmd.Run(); err != nil {
			d.errorf("Script %s failed: %v", script, err)
		}
	}

	return nil
}

func (d *Dispatcher) handleStateChange(ifaceName, adminState, operState string, force bool) error {
	iface, exists := d.interfacesByName[ifaceName]
	if !exists {
		return fmt.Errorf("unknown interface: %s", ifaceName)
	}

	changed := false
	var stateToRun string

	if adminState != "" && (force || adminState != iface.Administrative) {
		iface.Administrative = adminState
		changed = true
		stateToRun = adminState
	}

	if operState != "" && (force || operState != iface.Operational) {
		iface.Operational = operState
		changed = true
		stateToRun = operState
	}

	if changed || force {
		d.interfacesByName[ifaceName] = iface

		// Get interface data for broadcasting and address comparison
		data, _ := d.getInterfaceData(iface)

		// Check for IP address changes
		d.checkAddressChanges(ifaceName, data)

		// Broadcast the state change
		d.socketServer.BroadcastStateChange(
			ifaceName,
			iface.Index,
			iface.Operational,
			iface.Administrative,
			iface.Type,
			data,
		)

		// Run hooks for state changes
		if stateToRun != "" {
			d.runHooksForState(iface, stateToRun)
		}
	}

	return nil
}

func (d *Dispatcher) setupDBus() error {
	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("failed to connect to system bus: %v", err)
	}
	d.conn = conn

	// Subscribe to PropertiesChanged signals
	if err := conn.AddMatchSignal(
		dbus.WithMatchSender("org.freedesktop.network1"),
		dbus.WithMatchInterface("org.freedesktop.DBus.Properties"),
		dbus.WithMatchMember("PropertiesChanged"),
		dbus.WithMatchPathNamespace("/org/freedesktop/network1/link")); err != nil {
		return fmt.Errorf("failed to add match signal: %v", err)
	}

	return nil
}

func (d *Dispatcher) handleDBusSignal(signal *dbus.Signal) {
	if len(signal.Body) < 2 {
		return
	}

	interfaceName, ok := signal.Body[0].(string)
	if !ok || interfaceName != "org.freedesktop.network1.Link" {
		return
	}

	properties, ok := signal.Body[1].(map[string]dbus.Variant)
	if !ok {
		return
	}

	// Extract interface index from path - systemd uses special encoding
	// Path format: /org/freedesktop/network1/link/_3X where X is the interface index
	// The encoding converts the index to a hex representation
	pathStr := string(signal.Path)
	if !strings.HasPrefix(pathStr, "/org/freedesktop/network1/link/_") {
		d.logf("Unexpected signal path format: %s", pathStr)
		return
	}

	encodedIdx := pathStr[32:] // Skip "/org/freedesktop/network1/link/_"
	if len(encodedIdx) < 2 {
		return
	}

	// Decode the interface index from systemd's encoding
	// First two characters are hex representation of the first character
	firstChar, err := strconv.ParseInt(encodedIdx[:2], 16, 32)
	if err != nil {
		d.logf("Failed to parse interface index from path %s: %v", pathStr, err)
		return
	}

	// Combine with the rest of the string
	decodedStr := string(rune(firstChar)) + encodedIdx[2:]
	idx, err := strconv.Atoi(decodedStr)
	if err != nil {
		d.logf("Failed to convert decoded string %s to int: %v", decodedStr, err)
		return
	}

	ifaceName, exists := d.interfacesByIdx[idx]
	if !exists {
		d.logf("Unknown interface index %d, rescanning", idx)
		if err := d.scanInterfaces(); err != nil {
			d.errorf("Failed to rescan interfaces: %v", err)
			return
		}
		ifaceName, exists = d.interfacesByIdx[idx]
		if !exists {
			d.logf("Interface index %d still unknown after rescan, ignoring", idx)
			return
		}
	}

	var adminState, operState string

	if prop, ok := properties["AdministrativeState"]; ok {
		if s, ok := prop.Value().(string); ok {
			adminState = s
		}
	}

	if prop, ok := properties["OperationalState"]; ok {
		if s, ok := prop.Value().(string); ok {
			operState = s
		}
	}

	// Handle interface removal
	if adminState == "linger" {
		d.socketServer.BroadcastInterfaceRemoved(ifaceName, idx)
		delete(d.interfacesByIdx, idx)
		delete(d.interfacesByName, ifaceName)
		delete(d.previousIfaceData, ifaceName)
		return
	}

	// Always check for address changes on any property change, not just state changes
	if adminState != "" || operState != "" {
		d.logf("Interface %s (idx=%d) state change: admin=%s, oper=%s", ifaceName, idx, adminState, operState)
		if err := d.handleStateChange(ifaceName, adminState, operState, false); err != nil {
			d.errorf("Failed to handle state change for %s: %v", ifaceName, err)
		}
	} else {
		// Even if no state changed, check for IP address changes
		if iface, exists := d.interfacesByName[ifaceName]; exists {
			if data, err := d.getInterfaceData(iface); err == nil {
				d.checkAddressChanges(ifaceName, data)
			}
		}
	}
}

func (d *Dispatcher) Run(ctx context.Context, runStartupTriggers bool) error {
	return d.run(ctx, runStartupTriggers)
}

func (d *Dispatcher) run(ctx context.Context, runStartupTriggers bool) error {
	// Start socket server
	d.logf("Starting socket server...")
	if err := d.socketServer.Start(); err != nil {
		d.errorf("Socket server start failed: %v", err)
		return fmt.Errorf("failed to start socket server: %v", err)
	}

	// Set up cleanup function
	cleanup := func() {
		d.logf("Stopping socket server...")
		if err := d.socketServer.Stop(); err != nil {
			d.errorf("Socket server stop failed: %v", err)
		} else {
			d.logf("Socket server stopped successfully")
		}
	}
	defer cleanup()

	d.logf("Socket server started successfully")

	if err := d.scanInterfaces(); err != nil {
		return fmt.Errorf("initial interface scan failed: %v", err)
	}

	if err := d.setupDBus(); err != nil {
		return fmt.Errorf("D-Bus setup failed: %v", err)
	}

	if runStartupTriggers {
		d.logf("Running startup triggers for all interfaces")
		for _, iface := range d.interfacesByName {
			d.handleStateChange(iface.Name, iface.Administrative, iface.Operational, true)
		}
	}

	// Notify systemd that we're ready
	daemon.SdNotify(false, daemon.SdNotifyReady)

	signals := make(chan *dbus.Signal, 10)
	d.conn.Signal(signals)

	d.logf("Startup complete, listening for signals")

	for {
		select {
		case signal := <-signals:
			d.handleDBusSignal(signal)
		case <-ctx.Done():
			d.logf("Shutdown requested")
			return ctx.Err()
		}
	}
}
