package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// pubIP is a package-level 1-hour cache for the server's public IP.
var (
	pubIPMu    sync.Mutex
	pubIPValue string
	pubIPAt    time.Time
)

// detectLocalIP returns the IPv4 address of the interface holding the default
// route (the LAN address other hosts reach this server on). Scanning interfaces
// in order is only a fallback: on a Docker host it can pick a bridge address.
func detectLocalIP() string {
	// A UDP "connection" sends no packet; it only makes the kernel pick the
	// outbound interface.
	if conn, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer conn.Close()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && !addr.IP.IsLoopback() && !addr.IP.IsUnspecified() {
			return addr.IP.String()
		}
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				return ip.String()
			}
		}
	}
	return ""
}

// fetchPublicIP returns the server's public IP, cached for 1 hour.
func fetchPublicIP(ctx context.Context) string {
	pubIPMu.Lock()
	if time.Since(pubIPAt) < time.Hour && pubIPValue != "" {
		v := pubIPValue
		pubIPMu.Unlock()
		return v
	}
	pubIPMu.Unlock()

	ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, "GET", "https://api.ipify.org?format=text", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	ip := strings.TrimSpace(string(b))
	if net.ParseIP(ip) == nil {
		return ""
	}

	pubIPMu.Lock()
	pubIPValue = ip
	pubIPAt = time.Now()
	pubIPMu.Unlock()
	return ip
}

// resolveDnsServerIP returns the IP to use when provisioning DNS A records,
// based on the configured dnsServerIpMode. In "local" and "public" modes a
// failed detection is an error: silently falling back to another address would
// publish a DNS record pointing at the wrong host.
func (s *Server) resolveDnsServerIP(ctx context.Context) (string, error) {
	mode, _ := s.db.GetAppMeta("dns_server_ip_mode")
	switch mode {
	case "local":
		if ip := detectLocalIP(); ip != "" {
			return ip, nil
		}
		return "", errors.New("could not detect the server's local IP (DNS IP mode: local)")
	case "public":
		if ip := fetchPublicIP(ctx); ip != "" {
			return ip, nil
		}
		return "", errors.New("could not fetch the server's public IP (DNS IP mode: public)")
	default: // "custom" or unset
		if ip, err := s.db.GetAppMeta("dns_server_ip"); err == nil && ip != "" {
			return ip, nil
		}
	}
	return "192.0.2.10", nil // Hub default internal IP
}

type networkInfoResponse struct {
	LocalIP  string `json:"localIp"`
	PublicIP string `json:"publicIp"`
}

// getNetworkInfo returns the server's detected local and public IPs.
func (s *Server) getNetworkInfo(w http.ResponseWriter, r *http.Request) {
	localIP := detectLocalIP()
	publicIP := fetchPublicIP(r.Context())
	writeJSON(w, http.StatusOK, networkInfoResponse{LocalIP: localIP, PublicIP: publicIP})
}
