package web

import (
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

func netInterfaces() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				out = append(out, ip4.String())
			}
		}
	}
	return out, nil
}

func (s *Server) fetchExternalIP() string {
	s.mu.Lock()
	cached := s.extIP
	s.mu.Unlock()
	if cached != "" {
		return cached
	}
	client := &http.Client{Timeout: 4 * time.Second}
	for _, url := range []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://icanhazip.com",
	} {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		ip := trimSpace(string(data))
		if net.ParseIP(ip) != nil {
			s.mu.Lock()
			s.extIP = ip
			s.mu.Unlock()
			return ip
		}
	}
	return ""
}

func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

func (s *Server) WarmExternalIP() {
	s.warmExternalIP()
}

func (s *Server) warmExternalIP() {
	if ip := strings.TrimSpace(s.store.Get().ExternalIP); ip != "" {
		s.logger.Printf("Внешний IP (из конфига): %s", ip)
		return
	}
	ip := s.fetchExternalIP()
	if ip != "" {
		s.logger.Printf("Внешний IP: %s", ip)
	}
}
