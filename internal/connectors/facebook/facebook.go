package facebook

import "sync"

// Connector is a stub for the Facebook Messenger connector.
// Phase 1: UI and config only, real Graph API integration in Phase 2.
type Connector struct {
	mu        sync.RWMutex
	connected bool
	pageID    string
	pageName  string
	token     string
}

func New() *Connector {
	return &Connector{}
}

func (c *Connector) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *Connector) PageName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pageName
}

func (c *Connector) Connect(pageID, token string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// TODO: Validate token via graph.facebook.com/me?access_token=TOKEN
	c.pageID = pageID
	c.token = token
	c.pageName = "Facebook Page (stub)"
	c.connected = true
	return nil
}

func (c *Connector) Disconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = false
	c.pageID = ""
	c.pageName = ""
	c.token = ""
}
