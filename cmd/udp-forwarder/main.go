package main

import (
	"expvar"
	"log"
	"os"

	_ "net/http/pprof"

	"code.cloudfoundry.org/loggregator-agent/cmd/udp-forwarder/app"
	"code.cloudfoundry.org/loggregator-agent/pkg/metrics"
)

func main() {
	log := log.New(os.Stderr, "", log.LstdFlags)
	log.Println("starting UDP Forwarder...")
	defer log.Println("closing UDP Forwarder...")

	cfg := app.LoadConfig(log)
	m := metrics.New(expvar.NewMap("UDPForwarder"))

	forwarder := app.NewUDPForwarder(cfg, log, m)
	forwarder.Run()
}
