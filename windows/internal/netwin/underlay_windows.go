package netwin

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modiphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetBestInterfaceEx = modiphlpapi.NewProc("GetBestInterfaceEx")
	procGetNetworkParams   = modiphlpapi.NewProc("GetNetworkParams")

	modwlanapi            = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle    = modwlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle   = modwlanapi.NewProc("WlanCloseHandle")
	procWlanEnumIfaces    = modwlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryIface    = modwlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory    = modwlanapi.NewProc("WlanFreeMemory")
)

type InterfaceFact struct {
	Name  string   `json:"name"`
	Index int      `json:"index"`
	Type  string   `json:"type"`
	MTU   int      `json:"mtu,omitempty"`
	Addrs []string `json:"addresses,omitempty"`
}

type UnderlaySnapshot struct {
	DefaultInterfaceName  string          `json:"default_interface_name"`
	DefaultInterfaceIndex int             `json:"default_interface_index"`
	DefaultIPv4           string          `json:"default_ipv4,omitempty"`
	DNSServers            []string        `json:"dns_servers"`
	Interfaces            []InterfaceFact `json:"interfaces"`
	ConnectedSSID         string          `json:"connected_ssid,omitempty"`
	IsElevated            bool            `json:"is_elevated"`
	HasWintunDLL          bool            `json:"has_wintun_dll"`
}

type rawSockaddrInet struct {
	Family uint16
	Port   uint16
	Addr   [4]byte
	Zero   [8]byte
}

func SnapshotUnderlay() (*UnderlaySnapshot, error) {
	snap := &UnderlaySnapshot{
		IsElevated:    IsProcessElevated(),
		HasWintunDLL:  HasWintunDLL(),
		ConnectedSSID: QueryConnectedSSID(),
	}

	// Ask kernel routing table which interface reaches 1.1.1.1 (pure RIB lookup, sends 0 packets)
	dest := rawSockaddrInet{
		Family: windows.AF_INET,
		Addr:   [4]byte{1, 1, 1, 1},
	}
	var bestIfIndex uint32
	r1, _, _ := procGetBestInterfaceEx.Call(
		uintptr(unsafe.Pointer(&dest)),
		uintptr(unsafe.Pointer(&bestIfIndex)),
	)

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("enumerate network interfaces: %w", err)
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if strings.Contains(strings.ToLower(iface.Name), "xdial") {
			continue
		}
		addrs, _ := iface.Addrs()
		var addrStrs []string
		var firstV4 string
		for _, a := range addrs {
			s := a.String()
			addrStrs = append(addrStrs, s)
			if ipnet, ok := a.(*net.IPNet); ok && firstV4 == "" {
				if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() {
					firstV4 = v4.String()
				}
			}
		}
		if len(addrStrs) == 0 {
			continue
		}
		ifType := "other"
		lowerName := strings.ToLower(iface.Name)
		if strings.Contains(lowerName, "wi-fi") || strings.Contains(lowerName, "wlan") || strings.Contains(lowerName, "wireless") {
			ifType = "wifi"
		} else if strings.Contains(lowerName, "ethernet") || strings.Contains(lowerName, "以太网") {
			ifType = "ethernet"
		}
		snap.Interfaces = append(snap.Interfaces, InterfaceFact{
			Name:  iface.Name,
			Index: iface.Index,
			Type:  ifType,
			MTU:   iface.MTU,
			Addrs: addrStrs,
		})
		if r1 == 0 && int(bestIfIndex) == iface.Index {
			snap.DefaultInterfaceName = iface.Name
			snap.DefaultInterfaceIndex = iface.Index
			snap.DefaultIPv4 = firstV4
		} else if snap.DefaultInterfaceName == "" && firstV4 != "" {
			snap.DefaultInterfaceName = iface.Name
			snap.DefaultInterfaceIndex = iface.Index
			snap.DefaultIPv4 = firstV4
		}
	}

	snap.DNSServers = querySystemDNSServers()
	if len(snap.DNSServers) == 0 {
		snap.DNSServers = []string{"223.5.5.5", "1.1.1.1"}
	}
	return snap, nil
}

type ipAddrString struct {
	Next      *ipAddrString
	IpAddress [16]byte
	IpMask    [16]byte
	Context   uint32
}

type fixedInfo struct {
	HostName         [132]byte
	DomainName       [132]byte
	CurrentDnsServer *ipAddrString
	DnsServerList    ipAddrString
	NodeType         uint32
	ScopeId          [260]byte
	EnableRouting    uint32
	EnableProxy      uint32
	EnableDns        uint32
}

func querySystemDNSServers() []string {
	var size uint32
	r, _, _ := procGetNetworkParams.Call(0, uintptr(unsafe.Pointer(&size)))
	if r != uintptr(windows.ERROR_BUFFER_OVERFLOW) || size == 0 {
		return nil
	}
	buf := make([]byte, size)
	r, _, _ = procGetNetworkParams.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return nil
	}
	info := (*fixedInfo)(unsafe.Pointer(&buf[0]))
	seen := map[string]bool{}
	var out []string
	for cur := &info.DnsServerList; cur != nil; cur = cur.Next {
		raw := cur.IpAddress[:]
		n := 0
		for n < len(raw) && raw[n] != 0 {
			n++
		}
		ipStr := strings.TrimSpace(string(raw[:n]))
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if !seen[ipStr] {
			seen[ipStr] = true
			out = append(out, ipStr)
		}
	}
	return out
}

func IsProcessElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func HasWintunDLL() bool {
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "wintun.dll")); err == nil {
			return true
		}
	}
	if _, err := windows.LoadLibraryEx("wintun.dll", 0, windows.LOAD_LIBRARY_AS_DATAFILE); err == nil {
		return true
	}
	return false
}

// QueryConnectedSSID reads the currently associated Wi-Fi SSID using native wlanapi.dll
// without spawning netsh subprocesses or console windows.
func QueryConnectedSSID() string {
	if err := modwlanapi.Load(); err != nil {
		return ""
	}
	var negotiated uint32
	var clientHandle uintptr
	r, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&clientHandle)))
	if r != 0 || clientHandle == 0 {
		return ""
	}
	defer procWlanCloseHandle.Call(clientHandle, 0)

	var ifaceListPtr uintptr
	r, _, _ = procWlanEnumIfaces.Call(clientHandle, 0, uintptr(unsafe.Pointer(&ifaceListPtr)))
	if r != 0 || ifaceListPtr == 0 {
		return ""
	}
	defer procWlanFreeMemory.Call(ifaceListPtr)

	numItems := binary.LittleEndian.Uint32(unsafe.Slice((*byte)(unsafe.Pointer(ifaceListPtr)), 8)[0:4])
	if numItems == 0 {
		return ""
	}
	// WLAN_INTERFACE_INFO: InterfaceGuid (16 bytes) + strInterfaceDescription (256 UTF-16 = 512 bytes) + isState (4 bytes) = 532 bytes
	const infoSize = 532
	base := ifaceListPtr + 8
	for i := uint32(0); i < numItems && i < 8; i++ {
		itemPtr := base + uintptr(i)*infoSize
		state := binary.LittleEndian.Uint32(unsafe.Slice((*byte)(unsafe.Pointer(itemPtr+528)), 4))
		// wlan_interface_state_connected == 1
		if state != 1 {
			continue
		}
		var dataSize uint32
		var dataPtr uintptr
		// wlan_intf_opcode_current_connection == 7
		qr, _, _ := procWlanQueryIface.Call(
			clientHandle,
			itemPtr, // pointer to GUID
			7,
			0,
			uintptr(unsafe.Pointer(&dataSize)),
			uintptr(unsafe.Pointer(&dataPtr)),
			0,
		)
		if qr != 0 || dataPtr == 0 {
			continue
		}
		// WLAN_CONNECTION_ATTRIBUTES:
		// isState (4) + wlanConnectionMode (4) + strProfileName (512) = offset 520 for wlanAssociationAttributes
		// DOT11_SSID starts at offset 520: uSSIDLength (4 bytes) + ucSSID (32 bytes)
		if dataSize >= 556 {
			raw := unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), dataSize)
			ssidLen := binary.LittleEndian.Uint32(raw[520:524])
			if ssidLen > 0 && ssidLen <= 32 {
				ssid := string(raw[524 : 524+ssidLen])
				procWlanFreeMemory.Call(dataPtr)
				return ssid
			}
		}
		procWlanFreeMemory.Call(dataPtr)
	}
	_ = syscall.Errno(0)
	return ""
}
