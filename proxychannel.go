package proxychannel

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Proxychannel is a prxoy server that manages data transmission
// between http clients and destination servers.
// With the "Extensions" provided by user, Proxychannel is able to
// do authentication, communicate with databases, manipulate the
// requests/responses, etc.
type Proxychannel struct {
	extensionManager *ExtensionManager
	server           *http.Server
	waitGroup        *sync.WaitGroup
	serverDone       chan bool
}

// NewProxychannel returns a new Proxychannel
func NewProxychannel(hconf *HandlerConfig, sconf *ServerConfig, m map[string]Extension) *Proxychannel {
	pc := &Proxychannel{
		extensionManager: NewExtensionManager(m),
		waitGroup:        &sync.WaitGroup{},
		serverDone:       make(chan bool),
	}
	pc.server = NewServer(hconf, sconf, pc.extensionManager)
	return pc
}

// NewServer returns an http.Server that defined by user config
func NewServer(hconf *HandlerConfig, sconf *ServerConfig, em *ExtensionManager) *http.Server {
	// handler := NewProxy(WithoutDecryptHTTPS())
	handler := NewProxy(hconf, em)
	server := &http.Server{
		Addr:         sconf.ProxyAddr,
		Handler:      handler,
		ReadTimeout:  sconf.ReadTimeout,
		WriteTimeout: sconf.WriteTimeout,
		TLSConfig:    sconf.TLSConfig,
	}
	return server
}

func (pc *Proxychannel) runExtensionManager() {
	defer pc.waitGroup.Done()
	go pc.extensionManager.Setup() // TODO: modify setup and error handling
	signalChan := make(chan os.Signal, 1)
	signal.Notify(
		signalChan,
		syscall.SIGHUP,  // kill -SIGHUP XXXX
		syscall.SIGINT,  // kill -SIGINT XXXX or Ctrl+c
		syscall.SIGTERM, // kill -SIGTERM XXXX
		syscall.SIGQUIT, // kill -SIGQUIT XXXX
	)

	// Will block until shutdown signal is received
	<-signalChan
	Logger.Info("os.Interrupt captured by ExtensionManager, waiting for HTTP server to stop...\n")

	// Will block until pc.server has been shut down
	<-pc.serverDone
	Logger.Info("HTTP server has been shut down, Cleanup ExtensionManager...\n")
	pc.extensionManager.Cleanup()
	Logger.Info("Cleanup ExtensionManager done, ExtensionManager gracefully stopped!\n")
}

func (pc *Proxychannel) runServerBlocking(parentCtx context.Context) error {
	providedLifetimeCtx := parentCtx != nil
	if !providedLifetimeCtx {
		parentCtx = context.Background()
	}

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	defer close(pc.serverDone)

	pc.server.BaseContext = func(_ net.Listener) context.Context { return ctx }

	return pc.server.ListenAndServe()
}

func (pc *Proxychannel) runServer() {
	defer close(pc.serverDone)

	stop := func() {
		gracefulCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := pc.server.Shutdown(gracefulCtx); err != nil {
			Logger.Errorf("HTTP server Shutdown error: %v\n", err)
		} else {
			Logger.Info("HTTP server gracefully stopped\n")
		}
	}

	// Run server
	go func() {
		if err := pc.runServerBlocking(context.Background()); err != http.ErrServerClosed {
			Logger.Errorf("HTTP server ListenAndServe: %v", err)
			os.Exit(1)
		}
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(
		signalChan,
		syscall.SIGHUP,  // kill -SIGHUP XXXX
		syscall.SIGINT,  // kill -SIGINT XXXX or Ctrl+c
		syscall.SIGTERM, // kill -SIGTERM XXXX
		syscall.SIGQUIT, // kill -SIGQUIT XXXX
	)

	// Will block until shutdown signal is received
	<-signalChan
	Logger.Info("os.Interrupt captured by HTTP server, shutting down HTTP server...\n")

	// Terminate after second signal before callback is done
	go func() {
		<-signalChan
		Logger.Error("os.Interrupt captured twice by HTTP server, forcefully terminating HTTP server!\n")
		os.Exit(1)
	}()

	stop()
}

// RunContext launches the ExtensionManager and the HTTP server
func (pc *Proxychannel) RunContext(ctx context.Context) error {
	pc.waitGroup.Add(1)
	go pc.runExtensionManager()
	err := pc.runServerBlocking(ctx)
	pc.waitGroup.Wait()

	return err
}

// Run launches the ExtensionManager and the HTTP server
func (pc *Proxychannel) Run() {
	pc.waitGroup.Add(1)
	go pc.runExtensionManager()
	pc.runServer()
	pc.waitGroup.Wait()
}
