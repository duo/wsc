package wsc

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

type ProxyFunction func(req *http.Request) (*url.URL, error)

type ConnectedHandler func(*Client)

type ConnectionLostHandler func(*Client, error)

type ClientOption func(*ClientOptions)

type ClientOptions struct {
	Server    *url.URL
	TLSConfig *tls.Config

	ReadBufferSize  int
	WriteBufferSize int
	HTTPHeaders     http.Header
	Proxy           ProxyFunction

	KeepAlive   time.Duration
	PingTimeout time.Duration

	ConnectTimeout       time.Duration
	CloseTimeout         time.Duration
	MaxReconnectInterval time.Duration
	ConnectRetryInterval time.Duration

	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	OnConnected      ConnectedHandler
	OnConnectionLost ConnectionLostHandler
}

func NewClientOptions(server string, options ...func(*ClientOptions)) (*ClientOptions, error) {
	serverURI, err := url.Parse(server)
	if err != nil {
		return nil, err
	}

	o := ClientOptions{
		Server:               serverURI,
		HTTPHeaders:          make(map[string][]string),
		Proxy:                http.ProxyFromEnvironment,
		KeepAlive:            30 * time.Second,
		PingTimeout:          10 * time.Second,
		ConnectTimeout:       10 * time.Second,
		CloseTimeout:         3 * time.Second,
		MaxReconnectInterval: 1 * time.Minute,
		ConnectRetryInterval: 10 * time.Second,
		ReadTimeout:          0,
		WriteTimeout:         0,
	}

	for _, option := range options {
		option(&o)
	}

	return &o, nil
}

func ReadBufferSize(size int) ClientOption {
	return func(o *ClientOptions) {
		o.ReadBufferSize = size
	}
}

func WriteBufferSize(size int) ClientOption {
	return func(o *ClientOptions) {
		o.WriteBufferSize = size
	}
}

func HTTPHeaders(h http.Header) ClientOption {
	return func(o *ClientOptions) {
		o.HTTPHeaders = h
	}
}

func Proxy(p ProxyFunction) ClientOption {
	return func(o *ClientOptions) {
		o.Proxy = p
	}
}

func KeepAlive(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.KeepAlive = t
	}
}

func PingTimeout(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.PingTimeout = t
	}
}

func ConnectTimeout(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.ConnectTimeout = t
	}
}

func CloseTimeout(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.CloseTimeout = t
	}
}

func MaxReconnectInterval(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.MaxReconnectInterval = t
	}
}

func ConnectRetryInterval(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.ConnectRetryInterval = t
	}
}

func ReadTimeout(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.ReadTimeout = t
	}
}

func WriteTimeout(t time.Duration) ClientOption {
	return func(o *ClientOptions) {
		o.WriteTimeout = t
	}
}

func OnConnected(f ConnectedHandler) ClientOption {
	return func(o *ClientOptions) {
		o.OnConnected = f
	}
}

func OnConnectionLost(f ConnectionLostHandler) ClientOption {
	return func(o *ClientOptions) {
		o.OnConnectionLost = f
	}
}

func (o *ClientOptions) getDialer() *websocket.Dialer {
	return &websocket.Dialer{
		Proxy:            o.Proxy,
		HandshakeTimeout: o.ConnectTimeout,
		TLSClientConfig:  o.TLSConfig,
		ReadBufferSize:   o.ReadBufferSize,
		WriteBufferSize:  o.WriteBufferSize,
	}
}
