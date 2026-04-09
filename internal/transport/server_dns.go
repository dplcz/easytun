//go:build server

package transport

import (
	"easytun/internal/errorcode"
	"log"
	"net"

	"codeberg.org/miekg/dns"
	"codeberg.org/miekg/dns/dnsutil"
)

func (h *Hub) addDns(hostname string, ip net.IP) {
	h.dnsMtx.Lock()
	defer h.dnsMtx.Unlock()
	hostname = hostname + ".et."
	oldSnapshot := h.dnsMap.Load().(*dnsSnapshot)
	newSnapshot := &dnsSnapshot{
		dnsMap: make(map[string]net.IP),
	}
	for k, v := range oldSnapshot.dnsMap {
		newSnapshot.dnsMap[k] = v
	}
	newSnapshot.dnsMap[hostname] = ip
	h.dnsMap.Store(newSnapshot)
}

func (h *Hub) removeDns(hostname string) {
	h.dnsMtx.Lock()
	defer h.dnsMtx.Unlock()
	hostname = hostname + ".et."
	oldSnapshot := h.dnsMap.Load().(*dnsSnapshot)
	newSnapshot := &dnsSnapshot{
		dnsMap: make(map[string]net.IP),
	}
	for k, v := range oldSnapshot.dnsMap {
		if hostname == k {
			continue
		}
		newSnapshot.dnsMap[k] = v
	}
	h.dnsMap.Store(newSnapshot)
}

func (h *Hub) getDns(hostname string) net.IP {
	snapshot := h.dnsMap.Load().(*dnsSnapshot)
	log.Println(snapshot.dnsMap)
	if ip, ok := snapshot.dnsMap[hostname]; ok {
		return ip
	}
	return nil
}

func (h *Hub) buildDnsResponse(r []byte) ([]byte, error) {
	// TODO 注意是否是内存复用的
	msg := &dns.Msg{Data: r[2:]}
	if err := msg.Unpack(); err != nil {
		return nil, err
	}
	msgResp := msg.Copy()
	dnsutil.SetReply(msgResp, msg)
	hostname := msg.Question[0].Header().Name
	log.Println(hostname)
	dnsRes := h.getDns(hostname)
	msgResp.Authoritative = true
	if dnsRes == nil {
		msgResp.Rcode = dns.RcodeNameError
		err := msgResp.Pack()
		if err != nil {
			return nil, err
		}
		return msgResp.Data, nil
	}
	rr, err := dns.New(hostname + " 60 IN A " + dnsRes.String())
	if err != nil {
		return nil, errorcode.ParseDNSError
	}
	msgResp.Answer = append(msgResp.Answer, rr)
	err = msgResp.Pack()
	if err != nil {
		return nil, err
	}
	return append(r[:2], msgResp.Data...), nil
}
