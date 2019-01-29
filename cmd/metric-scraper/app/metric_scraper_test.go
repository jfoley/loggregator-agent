package app_test

import (
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"google.golang.org/grpc"

	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	"code.cloudfoundry.org/loggregator-agent/cmd/metric-scraper/app"
	"code.cloudfoundry.org/loggregator-agent/internal/testhelper"
	"code.cloudfoundry.org/loggregator-agent/pkg/plumbing"
)

var _ = Describe("App", func() {
	var (
		promServer  *httptest.Server
		spyAgent    *spyAgent
		dnsFilePath string
		scraper     *app.MetricScraper
		cfg         app.Config

		testLogger = log.New(GinkgoWriter, "", log.LstdFlags)
	)

	Describe("when configured with a single metrics_url", func() {
		BeforeEach(func() {
			spyAgent = newSpyAgent()
			promServer = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				Expect(req.URL.Path).To(Equal("/metrics"))

				w.Write([]byte(promOutput))
			}))

			tlsConfig, err := plumbing.NewServerMutualTLSConfig(
				testhelper.Cert("system-metrics-agent-ca.crt"),
				testhelper.Cert("system-metrics-agent-ca.key"),
				testhelper.Cert("system-metrics-agent-ca.crt"),
			)
			Expect(err).ToNot(HaveOccurred())

			promServer.TLS = tlsConfig

			promServer.StartTLS()

			u, err := url.Parse(promServer.URL)
			Expect(err).ToNot(HaveOccurred())

			scrapePort, err := strconv.Atoi(u.Port())
			Expect(err).ToNot(HaveOccurred())

			dnsFilePath = createDNSFile(u.Hostname())

			cfg = app.Config{
				ClientKeyPath:          testhelper.Cert("metron.key"),
				ClientCertPath:         testhelper.Cert("metron.crt"),
				CACertPath:             testhelper.Cert("loggregator-ca.crt"),
				MetricsKeyPath:         testhelper.Cert("system-metrics-agent-ca.key"),
				MetricsCertPath:        testhelper.Cert("system-metrics-agent-ca.crt"),
				MetricsCACertPath:      testhelper.Cert("system-metrics-agent-ca.crt"),
				MetricsCN:              "systemMetricsCA",
				LoggregatorIngressAddr: spyAgent.addr,
				ScrapeInterval:         100 * time.Millisecond,
				ScrapePort:             scrapePort,
				DefaultSourceID:        "default-id",
				DNSFile:                dnsFilePath,
				ShouldScrape:           true,
			}
		})

		AfterEach(func() {
			scraper.Stop()
			os.RemoveAll(filepath.Dir(dnsFilePath))
		})

		It("scrapes a prometheus endpoint and sends those metrics to a loggregator agent", func() {
			scraper = app.NewMetricScraper(cfg, testLogger)
			go scraper.Run()

			Eventually(spyAgent.Envelopes).Should(And(
				ContainElement(buildEnvelope("source-1", "node_timex_pps_calibration_total", 1)),
				ContainElement(buildEnvelope("source-1", "node_timex_pps_error_total", 2)),
				ContainElement(buildEnvelope("source-1", "node_timex_pps_frequency_hertz", 3)),
				ContainElement(buildEnvelope("source-2", "node_timex_pps_jitter_seconds", 4)),
				ContainElement(buildEnvelope("default-id", "node_timex_pps_jitter_total", 5)),
			))
		})

		It("doesn't scrape when ShouldScrape is false", func() {
			cfg.ShouldScrape = false
			scraper = app.NewMetricScraper(cfg, testLogger)
			go scraper.Run()

			Consistently(spyAgent.Envelopes, 1).Should(HaveLen(0))
		})
	})
})

func buildEnvelope(sourceID, name string, value float64) *loggregator_v2.Envelope {
	return &loggregator_v2.Envelope{
		SourceId: sourceID,
		Message: &loggregator_v2.Envelope_Gauge{
			Gauge: &loggregator_v2.Gauge{
				Metrics: map[string]*loggregator_v2.GaugeValue{
					name: {Value: value},
				},
			},
		},
	}
}

func createDNSFile(URL string) string {
	contents := fmt.Sprintf(dnsFileTemplate, URL)
	dir, err := ioutil.TempDir(".", "")
	if err != nil {
		log.Fatal(err)
	}

	tmpfn := filepath.Join(dir, "records.json")
	tmpfn, err = filepath.Abs(tmpfn)
	Expect(err).ToNot(HaveOccurred())

	if err := ioutil.WriteFile(tmpfn, []byte(contents), 0666); err != nil {
		log.Fatal(err)
	}
	return tmpfn
}

const (
	promOutput = `
# HELP node_timex_pps_calibration_total Pulse per second count of calibration intervals.
# TYPE node_timex_pps_calibration_total counter
node_timex_pps_calibration_total{source_id="source-1"} 1
# HELP node_timex_pps_error_total Pulse per second count of calibration errors.
# TYPE node_timex_pps_error_total counter
node_timex_pps_error_total{source_id="source-1"} 2
# HELP node_timex_pps_frequency_hertz Pulse per second frequency.
# TYPE node_timex_pps_frequency_hertz gauge
node_timex_pps_frequency_hertz{source_id="source-1"} 3
# HELP node_timex_pps_jitter_seconds Pulse per second jitter.
# TYPE node_timex_pps_jitter_seconds gauge
node_timex_pps_jitter_seconds{source_id="source-2"} 4
# HELP node_timex_pps_jitter_total Pulse per second count of jitter limit exceeded events.
# TYPE node_timex_pps_jitter_total counter
node_timex_pps_jitter_total 5
`
	dnsFileTemplate = `{
	"records": [
		[
			%q
		]
	]
}`
)

type spyAgent struct {
	loggregator_v2.IngressServer

	mu        sync.Mutex
	envelopes []*loggregator_v2.Envelope
	addr      string
}

func newSpyAgent() *spyAgent {
	agent := &spyAgent{}

	serverCreds, err := plumbing.NewServerCredentials(
		testhelper.Cert("metron.crt"),
		testhelper.Cert("metron.key"),
		testhelper.Cert("loggregator-ca.crt"),
	)
	if err != nil {
		panic(err)
	}

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		panic(err)
	}

	agent.addr = lis.Addr().String()

	grpcServer := grpc.NewServer(grpc.Creds(serverCreds))
	loggregator_v2.RegisterIngressServer(grpcServer, agent)

	go grpcServer.Serve(lis)

	return agent
}

func (s *spyAgent) BatchSender(srv loggregator_v2.Ingress_BatchSenderServer) error {
	for {
		batch, err := srv.Recv()
		if err != nil {
			return err
		}

		for _, e := range batch.GetBatch() {
			if e.GetTimestamp() == 0 {
				panic("0 timestamp!?")
			}

			// We want to make our lives easier for matching against envelopes
			e.Timestamp = 0
		}

		s.mu.Lock()
		s.envelopes = append(s.envelopes, batch.GetBatch()...)
		s.mu.Unlock()
	}
}

func (s *spyAgent) Envelopes() []*loggregator_v2.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()

	results := make([]*loggregator_v2.Envelope, len(s.envelopes))
	copy(results, s.envelopes)
	return results
}