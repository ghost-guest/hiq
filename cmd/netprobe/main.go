// Command netprobe is hiq's in-network probe binary (NETDEV_SPEC §5.1):
// run it ON a jump host inside the management network (copy it there manually
// for now — automated SFTP deployment ships later), point it at a CIDR, and
// it probes TCP ports and ICMP reachability from that network position — the
// only way to reach UDP/ICMP-dependent checks through a bastion (SSH tunnels
// are TCP-only). Output is JSON on stdout; nothing is installed, nothing
// persists: run-per-scan, delete-when-done.
//
// Usage:
//
//	netprobe -cidr 10.30.2.0/24 [-ports 22,23,161] [-icmp] [-concurrency 50]
//	netprobe -ip 10.30.2.5 -ports 22,23
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

type portResult struct {
	Port   int    `json:"port"`
	Open   bool   `json:"open"`
	Banner string `json:"banner,omitempty"`
}

type hostResult struct {
	IP    string       `json:"ip"`
	ICMP  bool         `json:"icmp,omitempty"`
	Ports []portResult `json:"ports,omitempty"`
}

func main() {
	cidr := flag.String("cidr", "", "target network, e.g. 10.30.2.0/24")
	ip := flag.String("ip", "", "single target host")
	portsFlag := flag.String("ports", "22,23,161", "comma-separated TCP ports")
	doICMP := flag.Bool("icmp", false, "ICMP echo sweep (needs raw-socket privileges on most OSes)")
	conc := flag.Int("concurrency", 50, "parallel probes")
	timeout := flag.Duration("timeout", 3*time.Second, "per-probe timeout")
	flag.Parse()

	var targets []string
	switch {
	case *ip != "":
		targets = []string{*ip}
	case *cidr != "":
		base, ipnet, err := net.ParseCIDR(*cidr)
		if err != nil {
			fatal("bad cidr: %v", err)
		}
		for cur := base.Mask(ipnet.Mask); ipnet.Contains(cur); inc(cur) {
			targets = append(targets, cur.String())
			if len(targets) > 65536 {
				fatal("cidr too large")
			}
		}
		if len(targets) > 2 && strings.Contains(targets[0], ".") {
			targets = targets[1 : len(targets)-1]
		}
	default:
		fatal("need -cidr or -ip")
	}

	var ports []int
	for _, p := range strings.Split(*portsFlag, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 && n < 65536 {
			ports = append(ports, n)
		}
	}

	jobs := make(chan string)
	results := make(chan hostResult, *conc)
	var wg sync.WaitGroup
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				hr := hostResult{IP: t}
				if *doICMP {
					hr.ICMP = icmpPing(t, *timeout)
				}
				for _, p := range ports {
					if pr := tcpProbe(t, p, *timeout); pr.Open {
						hr.Ports = append(hr.Ports, pr)
					}
				}
				if hr.ICMP || len(hr.Ports) > 0 {
					results <- hr
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, t := range targets {
			jobs <- t
		}
	}()
	go func() { wg.Wait(); close(results) }()

	out := make([]hostResult, 0, len(targets))
	for r := range results {
		out = append(out, r)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}

func tcpProbe(ip string, port int, timeout time.Duration) portResult {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(port)), timeout)
	if err != nil {
		return portResult{Port: port}
	}
	defer conn.Close()
	pr := portResult{Port: port, Open: true}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 96)
	if n, _ := conn.Read(buf); n > 0 {
		banner := strings.TrimSpace(string(buf[:n]))
		ok := true
		for _, r := range banner {
			if r < 0x20 && r != '\t' {
				ok = false
				break
			}
		}
		if ok && banner != "" {
			pr.Banner = banner
		}
	}
	return pr
}

func inc(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "netprobe: "+f+"\n", a...)
	os.Exit(2)
}

// icmpPing sends one ICMP echo via x/net/icmp. Raw ICMP requires elevated
// privileges on most platforms; without them it simply reports false (the
// TCP probes still work — run netprobe elevated when ICMP matters).
func icmpPing(ipStr string, timeout time.Duration) bool {
	dst, err := net.ResolveIPAddr("ip4", ipStr)
	if err != nil {
		return false
	}
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "netprobe: icmp unavailable (need privileges?): %v\n", err)
		return false
	}
	defer c.Close()

	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("hiq-netprobe")},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return false
	}
	if _, err := c.WriteTo(b, dst); err != nil {
		return false
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	rb := make([]byte, 1500)
	for {
		n, peer, err := c.ReadFrom(rb)
		if err != nil {
			return false
		}
		if peer.String() != dst.IP.String() {
			continue
		}
		rm, err := icmp.ParseMessage(1, rb[:n])
		if err != nil {
			continue
		}
		if rm.Type == ipv4.ICMPTypeEchoReply {
			return true
		}
	}
}
