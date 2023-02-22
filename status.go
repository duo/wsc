package wsc

import (
	"errors"
	"sync"
)

type status uint32

const (
	disconnected status = iota
	disconnecting
	connecting
	reconnecting
	connected
)

func (s status) String() string {
	switch s {
	case disconnected:
		return "disconnected"
	case disconnecting:
		return "disconnecting"
	case connecting:
		return "connecting"
	case reconnecting:
		return "reconnecting"
	case connected:
		return "connected"
	default:
		return "invalid"
	}
}

type connCompletedFn func(success bool) error
type disconnectCompletedFn func()
type connectionLostHandledFn func(bool) (connCompletedFn, error)

/*
disconnected -> `Connecting()` -> connecting -> `connCompletedFn(true)` -> connected
connected -> `Disconnecting()` -> disconnecting -> `disconnectCompletedFn()` -> disconnected
connected -> `ConnectionLost(false)` -> disconnecting -> `connectionLostHandledFn(true/false)` -> disconnected
connected -> `ConnectionLost(true)` -> disconnecting -> `connectionLostHandledFn(true)` -> connected
*/
var (
	errAbortConnection                = errors.New("disconnect called whist connection attempt in progress")
	errAlreadyConnectedOrReconnecting = errors.New("status is already connected or reconnecting")
	errStatusMustBeDisconnected       = errors.New("status can only transition to connecting from disconnected")
	errAlreadyDisconnected            = errors.New("status is already disconnected")
	errDisconnectionRequested         = errors.New("disconnection was requested whilst the action was in progress")
	errDisconnectionInProgress        = errors.New("disconnection already in progress")
)

type connectionStatus struct {
	sync.RWMutex

	status        status
	willReconnect bool

	actionCompleted chan struct{}
}

func (c *connectionStatus) ConnectionStatus() status {
	c.RLock()
	defer c.RUnlock()
	return c.status
}

func (c *connectionStatus) Connecting() (connCompletedFn, error) {
	c.Lock()
	defer c.Unlock()
	if c.status == connected || c.status == reconnecting {
		return nil, errAlreadyConnectedOrReconnecting
	}
	if c.status != disconnected {
		return nil, errStatusMustBeDisconnected
	}
	c.status = connecting
	c.actionCompleted = make(chan struct{})
	return c.connected, nil
}

func (c *connectionStatus) connected(success bool) error {
	c.Lock()
	defer func() {
		close(c.actionCompleted)
		c.actionCompleted = nil
		c.Unlock()
	}()

	if c.status == disconnecting {
		return errAbortConnection
	}

	if success {
		c.status = connected
	} else {
		c.status = disconnected
	}
	return nil
}

func (c *connectionStatus) Disconnecting() (disconnectCompletedFn, error) {
	c.Lock()
	if c.status == disconnected {
		c.Unlock()
		return nil, errAlreadyDisconnected
	}
	if c.status == disconnecting {
		c.willReconnect = false
		disConnectDone := c.actionCompleted
		c.Unlock()
		<-disConnectDone
		return nil, errAlreadyDisconnected
	}

	prevStatus := c.status
	c.status = disconnecting

	if prevStatus == connecting || prevStatus == reconnecting {
		connectDone := c.actionCompleted
		c.Unlock()
		<-connectDone

		if prevStatus == reconnecting && !c.willReconnect {
			return nil, errAlreadyDisconnected
		}
		c.Lock()
	}
	c.actionCompleted = make(chan struct{})
	c.Unlock()
	return c.disconnectionCompleted, nil
}

func (c *connectionStatus) disconnectionCompleted() {
	c.Lock()
	defer func() {
		close(c.actionCompleted)
		c.actionCompleted = nil
		c.Unlock()
	}()
	c.status = disconnected
}

func (c *connectionStatus) ConnectionLost(willReconnect bool) (connectionLostHandledFn, error) {
	c.Lock()
	defer c.Unlock()
	if c.status == disconnected {
		return nil, errAlreadyDisconnected
	}
	if c.status == disconnecting {
		return nil, errDisconnectionInProgress
	}

	c.willReconnect = willReconnect
	prevStatus := c.status
	c.status = disconnecting

	if prevStatus == connecting || prevStatus == reconnecting {
		connectDone := c.actionCompleted
		c.Unlock()
		<-connectDone
		c.Lock()
		if !willReconnect {
			return nil, errAlreadyDisconnected
		}
	}
	c.actionCompleted = make(chan struct{})

	return c.getConnectionLostHandler(willReconnect), nil
}

func (c *connectionStatus) getConnectionLostHandler(reconnectRequested bool) connectionLostHandledFn {
	return func(proceed bool) (connCompletedFn, error) {
		c.Lock()
		defer c.Unlock()

		if !c.willReconnect || !proceed {
			c.status = disconnected
			close(c.actionCompleted)
			c.actionCompleted = nil
			if !reconnectRequested || !proceed {
				return nil, nil
			}
			return nil, errDisconnectionRequested
		}

		c.status = reconnecting
		return c.connected, nil
	}
}
