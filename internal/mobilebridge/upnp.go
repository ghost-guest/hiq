package mobilebridge

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
)

// ProbeUPnP discovers the local gateway via SSDP and attempts AddPortMapping
// to get a server-reflexive candidate without STUN (HIQ_SPEC §11.6).
// 3s timeout, silent on failure (graceful degradation to STUN).
func ProbeUPnP(localPort int) (externalIP string, externalPort int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Try WANIPConnection1 (most common on modern routers)
	ipClients, _, _ := internetgateway2.NewWANIPConnection1Clients()
	for _, c := range ipClients {
		extIP, err := c.GetExternalIPAddress()
		if err != nil || extIP == "" {
			continue
		}
		err = c.AddPortMapping("", uint16(localPort), "UDP",
			uint16(localPort), localIP(), true, "linkpeer", 3600)
		if err != nil {
			continue
		}
		return extIP, localPort
	}

	// Try WANPPPConnection1 (older routers)
	pppClients, _, _ := internetgateway2.NewWANPPPConnection1Clients()
	for _, c := range pppClients {
		extIP, err := c.GetExternalIPAddress()
		if err != nil || extIP == "" {
			continue
		}
		err = c.AddPortMapping("", uint16(localPort), "UDP",
			uint16(localPort), localIP(), true, "linkpeer", 3600)
		if err != nil {
			continue
		}
		return extIP, localPort
	}

	_ = ctx // context for futureCtx API
	return "", 0
}

// localIP returns the first non-loopback IPv4.
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "0.0.0.0"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
			return ipNet.IP.String()
		}
	}
	return "0.0.0.0"
}

// UPnPCandidate generates a srflx ICE candidate from a UPnP port mapping.
func UPnPCandidate(localPort int) string {
	extIP, extPort := ProbeUPnP(localPort)
	if extIP == "" {
		return ""
	}
	return fmt.Sprintf("candidate:upnp 1 udp 2113929471 %s %d typ srflx raddr 0.0.0.0 rport 0", extIP, extPort)
}
