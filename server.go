package agentcli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	defaultServerAddress      = "127.0.0.1:8080"
	defaultServerRequestLimit = int64(1 << 20)
	defaultServerHeartbeat    = 15 * time.Second
	defaultServerTurnQueue    = 64
)

// ServerOption configures the HTTP API exposed by Agent.RunServer and
// NewServer. Server options are intentionally separate from Agent options.
type ServerOption func(*serverConfig) error

type serverConfig struct {
	address               string
	requestLimit          int64
	heartbeat             time.Duration
	turnQueue             int
	autoContinueSubagents bool
	middleware            []echo.MiddlewareFunc
}

func defaultServerConfig() serverConfig {
	return serverConfig{
		address:               defaultServerAddress,
		requestLimit:          defaultServerRequestLimit,
		heartbeat:             defaultServerHeartbeat,
		turnQueue:             defaultServerTurnQueue,
		autoContinueSubagents: true,
	}
}

// WithServerAutoContinueSubagents controls whether completed child turns
// automatically become trusted parent callback turns. It defaults to true so
// HTTP clients receive the same behavior as the reference terminal.
func WithServerAutoContinueSubagents(enabled bool) ServerOption {
	return func(config *serverConfig) error {
		config.autoContinueSubagents = enabled
		return nil
	}
}

// WithServerTurnQueueLimit bounds queued root turns per session. One active
// turn is not counted toward the limit. The default is 64.
func WithServerTurnQueueLimit(limit int) ServerOption {
	return func(config *serverConfig) error {
		if limit <= 0 {
			return errors.New("server turn queue limit must be positive")
		}
		config.turnQueue = limit
		return nil
	}
}

// WithServerAddress changes the listen address. The default is
// 127.0.0.1:8080 so the API is not exposed to the network accidentally.
func WithServerAddress(address string) ServerOption {
	return func(config *serverConfig) error {
		if address == "" {
			return errors.New("server address is required")
		}
		config.address = address
		return nil
	}
}

// WithServerRequestLimit changes the maximum JSON request body size.
func WithServerRequestLimit(bytes int64) ServerOption {
	return func(config *serverConfig) error {
		if bytes <= 0 {
			return errors.New("server request limit must be positive")
		}
		config.requestLimit = bytes
		return nil
	}
}

// WithServerHeartbeat changes the interval between SSE keepalive comments.
func WithServerHeartbeat(interval time.Duration) ServerOption {
	return func(config *serverConfig) error {
		if interval <= 0 {
			return errors.New("server heartbeat must be positive")
		}
		config.heartbeat = interval
		return nil
	}
}

// WithServerMiddleware adds an HTTP middleware. Middleware is applied in the
// same order it is supplied, so the first middleware is the outermost layer.
// Authentication, CORS, logging, and rate limiting belong here.
func WithServerMiddleware(middleware echo.MiddlewareFunc) ServerOption {
	return func(config *serverConfig) error {
		if middleware == nil {
			return errors.New("server middleware is required")
		}
		config.middleware = append(config.middleware, middleware)
		return nil
	}
}

// Server exposes an Agent as a JSON and server-sent-events HTTP API.
type Server struct {
	agent         *Agent
	config        serverConfig
	echo          *echo.Echo
	httpServer    *http.Server
	context       context.Context
	cancel        context.CancelFunc
	stopOnClose   func() bool
	shutdownOnce  sync.Once
	runMu         sync.Mutex
	runStarted    bool
	runsMu        sync.RWMutex
	turns         map[serverRunKey]*serverTurn
	activeTurns   map[string]*serverTurn
	pendingTurns  map[string][]*serverTurn
	sessionEvents *sessionEventHub
}

type serverRunKey struct {
	sessionID string
	turnID    string
}

// NewServer creates an HTTP server for agent without starting a listener.
// Handler can be used with an existing HTTP server or httptest.Server.
func NewServer(agent *Agent, options ...ServerOption) (*Server, error) {
	if agent == nil || agent.runtime == nil {
		return nil, errors.New("agent is required")
	}
	if agent.isClosing() {
		return nil, ErrClosed
	}
	config := defaultServerConfig()
	for index, option := range options {
		if option == nil {
			return nil, fmt.Errorf("agentcli server option %d is nil", index)
		}
		if err := option(&config); err != nil {
			return nil, fmt.Errorf("agentcli server option %d: %w", index, err)
		}
	}
	parentContext := agent.context
	if parentContext == nil {
		parentContext = context.Background()
	}
	serverContext, cancel := context.WithCancel(parentContext)
	stopOnClose := context.AfterFunc(agent.closing, cancel)
	server := &Server{
		agent:         agent,
		config:        config,
		context:       serverContext,
		cancel:        cancel,
		stopOnClose:   stopOnClose,
		turns:         make(map[serverRunKey]*serverTurn),
		activeTurns:   make(map[string]*serverTurn),
		pendingTurns:  make(map[string][]*serverTurn),
		sessionEvents: newSessionEventHub(),
	}
	server.echo = echo.New()
	server.echo.HideBanner = true
	server.echo.HidePort = true
	server.echo.HTTPErrorHandler = server.httpErrorHandler
	server.echo.Use(config.middleware...)
	server.routes()
	server.httpServer = &http.Server{
		Addr:              config.address,
		Handler:           server.echo,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return serverContext
		},
	}
	go func() {
		<-serverContext.Done()
		server.sessionEvents.close()
	}()
	if config.autoContinueSubagents {
		go server.continueSubagentCallbacks()
	}
	subagentConfirmations := agent.SubscribeSubagentConfirmations(serverContext)
	go server.forwardSubagentConfirmations(subagentConfirmations)
	subagentPermissions := agent.SubscribeSubagentPermissions(serverContext)
	go server.forwardSubagentPermissions(subagentPermissions)
	return server, nil
}

// Handler returns the complete HTTP API as an ordinary http.Handler.
func (server *Server) Handler() http.Handler {
	if server == nil || server.echo == nil {
		return http.NotFoundHandler()
	}
	return server.echo
}

// Echo returns the underlying Echo instance, allowing applications to add
// application-specific routes or use Echo's normal integrations. Add routes
// before Run is called.
func (server *Server) Echo() *echo.Echo {
	if server == nil {
		return nil
	}
	return server.echo
}

// Run listens until Shutdown is called or the Agent is closed.
func (server *Server) Run() error {
	if server == nil || server.httpServer == nil {
		return errors.New("server is nil")
	}
	if err := server.context.Err(); err != nil {
		return ErrClosed
	}
	server.runMu.Lock()
	if server.runStarted {
		server.runMu.Unlock()
		return errors.New("server has already been run")
	}
	server.runStarted = true
	server.runMu.Unlock()

	listenerDone := make(chan struct{})
	go func() {
		select {
		case <-server.context.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.httpServer.Shutdown(shutdownContext)
		case <-listenerDone:
		}
	}()
	err := server.httpServer.ListenAndServe()
	close(listenerDone)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops the HTTP listener and cancels runs started by
// this server. It does not close the Agent, which may still be used directly.
func (server *Server) Shutdown(ctx context.Context) error {
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server.shutdownOnce.Do(func() {
		server.stopOnClose()
		server.cancel()
	})
	if server.httpServer == nil {
		return nil
	}
	return server.httpServer.Shutdown(ctx)
}

// RunServer creates and runs the default local HTTP API. It blocks until the
// Agent is closed, the server is shut down, or the listener fails.
func (agent *Agent) RunServer(options ...ServerOption) error {
	server, err := NewServer(agent, options...)
	if err != nil {
		return err
	}
	return server.Run()
}

func (server *Server) findTurn(sessionID, turnID string) (*serverTurn, bool) {
	server.runsMu.RLock()
	turn, found := server.turns[serverRunKey{sessionID: sessionID, turnID: turnID}]
	server.runsMu.RUnlock()
	return turn, found
}
