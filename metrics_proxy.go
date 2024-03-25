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

		cert, err := tls.LoadX509KeyPair(c.etcdCert, c.etcdKey)
		if err != nil {
			log.Fatal(err)
		}

		proxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      pool,
				Certificates: []tls.Certificate{cert},
				ServerName:   c.upstreamServerName,
			},
		}
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
