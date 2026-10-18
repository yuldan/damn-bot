// Package bad holds positive cases: every construct below violates E-05.
package bad

import (
	"net"
	nethttp "net/http"
	"net/url"
	"time"
)

type clientAlias = nethttp.Client

func clientValue() nethttp.Client {
	return nethttp.Client{Timeout: time.Second} // want `nohttpclient: E-05: composite literal of http.Client`
}

func clientPointer() *nethttp.Client {
	return &nethttp.Client{} // want `nohttpclient: E-05: composite literal of http.Client`
}

func clientViaAlias() *clientAlias {
	return &clientAlias{} // want `composite literal of http.Client`
}

func clientNew() *nethttp.Client {
	return new(nethttp.Client) // want `nohttpclient: E-05: new\(http.Client\)`
}

func transport() nethttp.RoundTripper {
	return &nethttp.Transport{MaxIdleConns: 1} // want `composite literal of http.Transport`
}

func elided() []*nethttp.Transport {
	return []*nethttp.Transport{{}} // want `composite literal of http.Transport`
}

func defaults() {
	_ = nethttp.DefaultClient    // want `nohttpclient: E-05: use of http.DefaultClient`
	_ = nethttp.DefaultTransport // want `use of http.DefaultTransport`
}

func defaultMethodValue() func(*nethttp.Request) (*nethttp.Response, error) {
	return nethttp.DefaultClient.Do // want `use of http.DefaultClient`
}

func helpers(u string) {
	_, _ = nethttp.Get(u)                     // want `use of http.Get`
	_, _ = nethttp.Head(u)                    // want `use of http.Head`
	_, _ = nethttp.Post(u, "text/plain", nil) // want `use of http.Post`
	_, _ = nethttp.PostForm(u, url.Values{})  // want `use of http.PostForm`
	get := nethttp.Get                        // want `use of http.Get`
	_ = get
}

func dials() {
	_, _ = net.Dial("tcp", "x:1")                     // want `use of net.Dial`
	_, _ = net.DialTimeout("tcp", "x:1", time.Second) // want `use of net.DialTimeout`
	_, _ = net.DialTCP("tcp", nil, &net.TCPAddr{})    // want `use of net.DialTCP`
	_, _ = net.DialUDP("udp", nil, &net.UDPAddr{})    // want `use of net.DialUDP`
	_, _ = net.DialUnix("unix", nil, &net.UnixAddr{}) // want `use of net.DialUnix`
	_, _ = net.DialIP("ip4:icmp", nil, &net.IPAddr{}) // want `use of net.DialIP`
	d := net.Dialer{Timeout: time.Second}             // want `composite literal of net.Dialer`
	_ = d
}
