package server

import (
	"net"
	"os"
	"strconv"

	"github.com/grandcat/zeroconf"
)

const mdnsServiceType = "_llamaswap._tcp"
const mdnsDomain = "local."

// RegisterMDNS advertises this llama-swap instance on the LAN via mDNS/Bonjour
// under the service type _llamaswap._tcp.local. The registration is withdrawn
// when the server shuts down.
//
// Call after the HTTP listener is bound so the correct port is announced.
func (s *Server) RegisterMDNS(listenAddr string) {
	if listenAddr == "" {
		return
	}
	_, portStr, err := net.SplitHostPort(listenAddr)
	if err != nil {
		s.proxylog.Warnf("mDNS: cannot parse listen addr %q: %v", listenAddr, err)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		s.proxylog.Warnf("mDNS: invalid port in %q", listenAddr)
		return
	}

	hostname, _ := os.Hostname()
	txt := []string{
		"version=" + s.build.Version,
		"host=" + hostname,
	}

	srv, err := zeroconf.Register(hostname, mdnsServiceType, mdnsDomain, port, txt, nil)
	if err != nil {
		s.proxylog.Warnf("mDNS: failed to register service: %v", err)
		return
	}

	s.proxylog.Infof("mDNS: registered %s as %s on port %d", hostname, mdnsServiceType, port)

	go func() {
		<-s.shutdownCtx.Done()
		srv.Shutdown()
		s.proxylog.Debugf("mDNS: service unregistered")
	}()
}
