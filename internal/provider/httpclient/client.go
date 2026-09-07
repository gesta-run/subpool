package httpclient

import (
	"net"
	"net/http"
	"time"
)

const defaultResponseHeaderTimeout = 3 * time.Minute

func New() *http.Client {
	return NewWithResponseHeaderTimeout(defaultResponseHeaderTimeout)
}

func NewWithResponseHeaderTimeout(responseHeaderTimeout time.Duration) *http.Client {
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = defaultResponseHeaderTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport}
}
