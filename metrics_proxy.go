package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type config struct {
	port               int
	upstreamHost       string
	upstreamPort       int
	upstreamServerName string
	etcdCA             string
	etcdCert           string
	etcdKey            string
}

func initFlags(c *config) {
	flag.IntVar(&c.port, "port", 2381, "Port to bind to.")
	flag.StringVar(&c.upstreamHost, "upstream-host", "localhost", "The upstream etcd host.")
	flag.IntVar(&c.upstreamPort, "upstream-port", 2379, "The upstream etcd port.")
	flag.StringVar(&c.upstreamServerName, "upstream-server-name", "localhost", "The upstream tls server name.")
	flag.StringVar(&c.etcdCA, "etcd-ca", "", "The CA file for etcd tls.")
	flag.StringVar(&c.etcdCert, "etcd-cert", "", "The cert file for etcd tls.")
	flag.StringVar(&c.etcdKey, "etcd-key", "", "The key file for etcd tls.")
}

func validateFlags(c *config) {
	if len(c.etcdCA) == 0 {
		log.Fatal("--etcd-ca=<ca-file> is required")
	}
	if len(c.etcdCert) == 0 {
		log.Fatal("--etcd-cert=<cert-file> is required")
	}
	if len(c.etcdKey) == 0 {
		log.Fatal("--etcd-key=<key-file> is required")
	}
}

func main() {
	c := config{}
	initFlags(&c)
	flag.Parse()
	validateFlags(&c)

	var tryHttp bool

	pool := x509.NewCertPool()
	capem, err := os.ReadFile(c.etcdCA)
	if err != nil {
		log.Println(err)
		tryHttp = true
	}

	var scheme string
	var host string
	if tryHttp {
		scheme = "http"
		host = fmt.Sprintf("%s:%d", c.upstreamHost, c.port)
	} else {
		scheme = "https"
		host = fmt.Sprintf("%s:%d", c.upstreamHost, c.upstreamPort)
	}

	log.Printf("will proxy: %s://%s", scheme, host)
	proxy := httputil.NewSingleHostReverseProxy(&url.URL{
		Scheme: scheme,
		Host:   host,
	})

	if !tryHttp {
		if !pool.AppendCertsFromPEM(capem) {
			log.Fatal("error: failed to add ca to cert pool")
		}

		initialTransport, err := buildHTTPSTransport(pool, c.etcdCert, c.etcdKey, c.upstreamServerName)
		if err != nil {
			log.Fatal(err)
		}

		switcher := &transportSwitcher{}
		switcher.Store(initialTransport)
		proxy.Transport = switcher

		go watchAndReloadTLS(c, switcher)
	}

	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		log.Printf("server: proxy metrics request to etcd")
		director(req)
	}

	server := http.NewServeMux()
	server.Handle("/metrics", proxy)
	server.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})

	addr := fmt.Sprintf(":%d", c.port)
	log.Printf("server: listening on %s\n", addr)
	if err := http.ListenAndServe(addr, server); err != nil {
		log.Fatal(err)
	}
}

// transportSwitcher implements http.RoundTripper and atomically swaps the underlying *http.Transport.
type transportSwitcher struct {
	v atomic.Value // holds *http.Transport
}

func (s *transportSwitcher) RoundTrip(r *http.Request) (*http.Response, error) {
	t, _ := s.v.Load().(*http.Transport)
	if t == nil {
		return http.DefaultTransport.RoundTrip(r)
	}
	return t.RoundTrip(r)
}

func (s *transportSwitcher) Store(t *http.Transport) {
	s.v.Store(t)
}

// buildHTTPSTransport creates a new *http.Transport with the given cert pool and client cert/key.
func buildHTTPSTransport(rootPool *x509.CertPool, certPath, keyPath, serverName string) (*http.Transport, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	tlsConf := &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{cert},
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}

	return &http.Transport{
		TLSClientConfig:       tlsConf,
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}, nil
}

// watchAndReloadTLS watches etcd-ca, etcd-cert, and etcd-key for changes and rebuilds the transport.
func watchAndReloadTLS(c config, switcher *transportSwitcher) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("tls-reload: failed to create watcher: %v", err)
		return
	}
	defer watcher.Close()

	// Deduplicate directories to watch
	targets := []string{c.etcdCA, c.etcdCert, c.etcdKey}
	dirSet := map[string]struct{}{}
	baseByDir := map[string]map[string]struct{}{}
	for _, p := range targets {
		dir := filepath.Dir(p)
		base := filepath.Base(p)
		dirSet[dir] = struct{}{}
		if baseByDir[dir] == nil {
			baseByDir[dir] = map[string]struct{}{}
		}
		baseByDir[dir][base] = struct{}{}
	}

	for dir := range dirSet {
		if err := watcher.Add(dir); err != nil {
			log.Printf("tls-reload: failed to watch dir %s: %v", dir, err)
		} else {
			log.Printf("tls-reload: watching %s", dir)
		}
	}

	// Debounce timer to avoid excessive reloads during atomic updates (e.g., symlink swaps)
	var reloadTimer *time.Timer
	scheduleReload := func() {
		if reloadTimer != nil {
			reloadTimer.Stop()
		}
		reloadTimer = time.AfterFunc(250*time.Millisecond, func() {
			performReload(c, switcher)
		})
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// Only react to changes for our target basenames in watched dirs
			dir := filepath.Dir(event.Name)
			base := filepath.Base(event.Name)
			if m, exists := baseByDir[dir]; exists {
				if _, target := m[base]; target {
					switch event.Op {
					case fsnotify.Create, fsnotify.Write, fsnotify.Remove, fsnotify.Rename, fsnotify.Chmod:
						log.Printf("tls-reload: detected change: %s (%s)", event.Name, event.Op)
						scheduleReload()
					}
				}
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("tls-reload: watcher error: %v", err)
		}
	}
}

func performReload(c config, switcher *transportSwitcher) {
	// Rebuild root pool
	capem, err := os.ReadFile(c.etcdCA)
	if err != nil {
		log.Printf("tls-reload: read ca failed: %v", err)
		return
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(capem) {
		log.Printf("tls-reload: failed to add ca to cert pool")
		return
	}

	// Build new transport
	newTransport, err := buildHTTPSTransport(pool, c.etcdCert, c.etcdKey, c.upstreamServerName)
	if err != nil {
		log.Printf("tls-reload: rebuild transport failed: %v", err)
		return
	}

	// Close idle connections on the old transport before swap
	if old, _ := switcher.v.Load().(*http.Transport); old != nil {
		old.CloseIdleConnections()
	}

	switcher.Store(newTransport)
	log.Printf("tls-reload: TLS configuration reloaded successfully")
}
