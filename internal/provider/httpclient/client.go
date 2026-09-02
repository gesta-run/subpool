package httpclient

import (
	"net"
	"net/http"
	"time"
)

func New() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 60 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport}
}
