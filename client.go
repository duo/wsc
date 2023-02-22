package wsc

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	log "github.com/sirupsen/logrus"
)

var ErrNotConnected = errors.New("websocket not connected")

type Client struct {
	options *ClientOptions

	status connectionStatus

	conn   *websocket.Conn
	connMu sync.Mutex

	rio sync.Mutex
	wio sync.Mutex

	lastPong atomic.Value
	stopPing chan struct{}

	shortCircuitReconnect chan struct{}
}

func NewClient(o *ClientOptions) *Client {
	return &Client{options: o}
}

func (c *Client) IsConnected() bool {
	return c.status.ConnectionStatus() == connected
}

func (c *Client) Connect() error {
	log.Debugln("Enter connect")

	connectionUp, err := c.status.Connecting()
	if err != nil {
		if err == errAlreadyConnectedOrReconnecting {
			log.Warnf("Connect() called but not disconnected")
			return nil
		}
		return err
	}

RETRYCONN:
	conn, err := c.attemptConnection()
	if err != nil {
		log.Infof("Failed to connect, sleeping for %s and retry, error: %v", c.options.ConnectRetryInterval, err)
		time.Sleep(c.options.ConnectRetryInterval)

		if c.status.ConnectionStatus() == connecting { // Possible connection aborted elsewhere
			goto RETRYCONN
		}

		if err := connectionUp(false); err != nil {
			log.Errorf("Failed to connection up: %v", err)
		}

		log.Errorf("Failed to connect to server")
		return err
	}

	c.initialize(conn, connectionUp)

	return nil
}

func (c *Client) Disconnect() {
	log.Debugln("Enter disconnect")

	select {
	case c.shortCircuitReconnect <- struct{}{}:
	default:
	}

	disDone, err := c.status.Disconnecting()
	if err != nil {
		log.Warnf("Failed to disconnect: %v", err)
		return
	}

	defer func() {
		c.dispose()
		disDone()
		log.Debugln("Disconnected")
	}()

	{
		c.connMu.Lock()
		defer c.connMu.Unlock()
		if c.conn != nil {
			c.wio.Lock()
			defer c.wio.Unlock()
			log.Debugln("Send close message to websocket")
			msg := websocket.FormatCloseMessage(websocket.CloseGoingAway, "")
			err := c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(c.options.CloseTimeout))
			if err != nil && err != websocket.ErrCloseSent {
				log.Warnf("Error sent close message to websocket: %v", err)
			}
		}
	}
}

func (c *Client) ReadMessage() (messageType int, message []byte, err error) {
	err = ErrNotConnected

	if c.IsConnected() {
		c.rio.Lock()
		if c.options.ReadTimeout != 0 {
			c.conn.SetReadDeadline(time.Now().Add(c.options.ReadTimeout))
		}
		messageType, message, err = c.conn.ReadMessage()
		c.rio.Unlock()
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			c.Disconnect()
			return messageType, message, nil
		}
		if err != nil {
			c.connectionLost(err)
		}
	}

	return
}

func (c *Client) ReadJSON(v any) (err error) {
	err = ErrNotConnected

	if c.IsConnected() {
		c.rio.Lock()
		if c.options.ReadTimeout != 0 {
			c.conn.SetReadDeadline(time.Now().Add(c.options.ReadTimeout))
		}
		err = c.conn.ReadJSON(&v)
		c.rio.Unlock()
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			c.Disconnect()
			return nil
		}
		if err != nil {
			c.connectionLost(err)
		}
	}

	return
}

func (c *Client) WriteMessage(messageType int, data []byte) error {
	err := ErrNotConnected

	if c.IsConnected() {
		c.wio.Lock()
		if c.options.WriteTimeout != 0 {
			c.conn.SetWriteDeadline(time.Now().Add(c.options.WriteTimeout))
		}
		err = c.conn.WriteMessage(messageType, data)
		c.wio.Unlock()
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			c.Disconnect()
			return nil
		}
		if err != nil {
			c.connectionLost(err)
		}
	}

	return err
}

func (c *Client) WriteJSON(v any) error {
	err := ErrNotConnected

	if c.IsConnected() {
		c.wio.Lock()
		if c.options.WriteTimeout != 0 {
			c.conn.SetWriteDeadline(time.Now().Add(c.options.WriteTimeout))
		}
		err = c.conn.WriteJSON(v)
		c.wio.Unlock()
		if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
			c.Disconnect()
			return nil
		}
		if err != nil {
			c.connectionLost(err)
		}
	}

	return err
}

func (c *Client) connectionLost(reason error) {
	log.Debugln("Enter connection lost")

	disDone, err := c.status.ConnectionLost(c.status.ConnectionStatus() > connecting)
	if err != nil && err != errDisconnectionInProgress {
		log.Warnf("Connection lost unexpected status: %v", err)
		return
	}

	c.dispose()

	go func() {
		reConnDone, err := disDone(true)
		if err != nil {
			log.Errorf("Failed to report completion of disconnect: %v", err)
		}

		go c.reconnect(reConnDone)

		if c.options.OnConnectionLost != nil {
			go c.options.OnConnectionLost(c, reason)
		}
	}()
}

func (c *Client) reconnect(connectionUp connCompletedFn) {
	log.Debugln("Enter reconnect")

	var conn *websocket.Conn

	c.shortCircuitReconnect = make(chan struct{})
	interval := c.options.ConnectRetryInterval / 2

	for {
		var err error
		conn, err = c.attemptConnection()
		if err == nil {
			break
		}

		interval *= 2
		if interval > c.options.MaxReconnectInterval {
			interval = c.options.MaxReconnectInterval
		}
		log.Debugf("Reconnect failed, sleep for %s: %v", interval, err)

		select {
		case <-c.shortCircuitReconnect:
			log.Debugln("Reconnect was short-circuited")
		case <-time.After(interval):
		}

		if c.status.ConnectionStatus() != reconnecting { // Disconnect may have been called
			if err := connectionUp(false); err != nil { // Should always return an error
				log.Warnf("Failed to connection up: %v", err)
			}
			log.Debugln("Client moved to disconnected state while reconnecting, abandoning reconnect")
			return
		}
	}

	c.initialize(conn, connectionUp)
}

func (c *Client) attemptConnection() (*websocket.Conn, error) {
	dialer := c.options.getDialer()

	ws, resp, err := dialer.Dial(c.options.Server.String(), c.options.HTTPHeaders)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			if body, err := io.ReadAll(resp.Body); err != nil {
				return nil, fmt.Errorf("websocket handshake failed, code: %d: %v", resp.StatusCode, err)
			} else {
				return nil, fmt.Errorf("websocket handshake failed, code: %d, body: %s", resp.StatusCode, body)
			}
		}
		return nil, err
	} else {
		return ws, nil
	}
}

func (c *Client) initialize(conn *websocket.Conn, connectionUp connCompletedFn) bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn != nil {
		log.Warnf("Previous connection not closed")
		_ = conn.Close()
		if err := connectionUp(false); err != nil {
			log.Errorf("Failed to connection up: %v", err)
		}
		return false
	}
	c.conn = conn

	c.stopPing = make(chan struct{})
	c.lastPong.Store(time.Now())
	go c.keepAlive()

	if err := connectionUp(true); err != nil {
		select {
		case c.stopPing <- struct{}{}:
		default:
		}
		_ = c.conn.Close()
		c.conn = nil
		return false
	}

	log.Debugf("Client is connected to %s", c.options.Server)
	if c.options.OnConnected != nil {
		go c.options.OnConnected(c)
	}

	return true
}

func (c *Client) dispose() {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	select {
	case c.stopPing <- struct{}{}:
	default:
	}

	if c.conn == nil {
		log.Debugf("Client not running")
	} else {
		_ = c.conn.Close()
		c.conn = nil
	}
}

func (c *Client) keepAlive() {
	log.Debugln("Keep-Alive starting")

	c.conn.SetPongHandler(func(msg string) error {
		log.Debugln("Receive pong message")
		c.lastPong.Store(time.Now())
		return nil
	})

	ticker := time.NewTicker(c.options.KeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopPing:
			log.Debugln("Keep-Alive stopped")
			return
		case <-ticker.C:
			if !c.IsConnected() {
				continue
			}

			c.wio.Lock()
			log.Debugln("Send ping message to websocket")
			err := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(c.options.PingTimeout))
			c.wio.Unlock()
			if err != nil {
				log.Warnf("Failed to send ping message: %v", err)
			}

			lastPong := c.lastPong.Load().(time.Time)
			if time.Since(lastPong) > c.options.KeepAlive*2 {
				c.connectionLost(errors.New("ping response not received, disconnecting"))
				return
			}
		}
	}
}
