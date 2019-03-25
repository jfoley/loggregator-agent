package app_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/go-loggregator"
	"code.cloudfoundry.org/go-loggregator/rpc/loggregator_v2"
	"code.cloudfoundry.org/loggregator-agent/cmd/forwarder-agent/app"
	"code.cloudfoundry.org/loggregator-agent/internal/testhelper"
	"code.cloudfoundry.org/loggregator-agent/pkg/plumbing"
	"github.com/gogo/protobuf/proto"
	"github.com/onsi/gomega/gexec"
	"google.golang.org/grpc"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const forwardConfigTemplate = `---
ingress: %s
`

var (
	fConfigDir string
)

var _ = Describe("Main", func() {
	var (
		grpcPort   = 20000
		testLogger = log.New(GinkgoWriter, "", log.LstdFlags)

		forwarderAgent *app.ForwarderAgent
		mc             *testhelper.SpyMetricClientV2
		cfg            app.Config
		ingressClient  *loggregator.IngressClient

		emitEnvelopes = func(ctx context.Context, d time.Duration, wg *sync.WaitGroup) {
			go func() {
				defer wg.Done()

				ticker := time.NewTicker(d)
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						ingressClient.Emit(sampleEnvelope)
					}
				}
			}()
		}

		emitCounters = func(ctx context.Context, d time.Duration, wg *sync.WaitGroup) {
			go func() {
				defer wg.Done()

				ticker := time.NewTicker(d)
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						ingressClient.Emit(sampleCounter)
					}
				}
			}()
		}
	)

	BeforeEach(func() {
		fConfigDir = forwarderPortConfigDir()

		mc = testhelper.NewMetricClientV2()
		cfg = app.Config{
			GRPC: app.GRPC{
				Port:     uint16(grpcPort),
				CAFile:   testhelper.Cert("loggregator-ca.crt"),
				CertFile: testhelper.Cert("metron.crt"),
				KeyFile:  testhelper.Cert("metron.key"),
			},
			DownstreamIngressPortCfg: fmt.Sprintf("%s/*/ingress_port.yml", fConfigDir),
			DebugPort:                7392,
			Tags: map[string]string{
				"some-tag": "some-value",
			},
		}
		ingressClient = newIngressClient(grpcPort)
	})

	AfterEach(func() {
		os.RemoveAll(fConfigDir)

		gexec.CleanupBuildArtifacts()
		grpcPort++
	})

	It("has a dropped metric with direction", func() {
		forwarderAgent = app.NewForwarderAgent(cfg, mc, testLogger)
		go forwarderAgent.Run()

		et := map[string]string {
			"direction": "ingress",
		}

		Eventually(func() bool {
			return mc.HasMetric("dropped", et)
		}).Should(BeTrue())

		m := mc.GetMetric("dropped", et)

		Expect(m).ToNot(BeNil())
		Expect(m.Opts.ConstLabels).To(HaveKeyWithValue("direction", "ingress"))
	})

	It("forwards all envelopes downstream", func() {
		downstream1 := startSpyLoggregatorV2Ingress()
		downstream2 := startSpyLoggregatorV2Ingress()

		forwarderAgent = app.NewForwarderAgent(cfg, mc, testLogger)
		go forwarderAgent.Run()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()

		wg.Add(1)
		emitEnvelopes(ctx, 10*time.Millisecond, &wg)

		var e1, e2 *loggregator_v2.Envelope
		Eventually(downstream1.envelopes, 5).Should(Receive(&e1))
		Eventually(downstream2.envelopes, 5).Should(Receive(&e2))

		Expect(proto.Equal(e1, sampleEnvelope)).To(BeTrue())
		Expect(proto.Equal(e2, sampleEnvelope)).To(BeTrue())
	})

	It("aggregates counter events before forwarding downstream", func() {
		downstream1 := startSpyLoggregatorV2Ingress()

		forwarderAgent = app.NewForwarderAgent(cfg, mc, testLogger)
		go forwarderAgent.Run()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()

		wg.Add(1)
		emitCounters(ctx, 10*time.Millisecond, &wg)

		var e1 *loggregator_v2.Envelope
		Eventually(downstream1.envelopes, 5).Should(Receive(&e1))

		Expect(e1.GetCounter().GetTotal()).To(Equal(uint64(20)))
	})

	It("tags before forwarding downstream", func() {
		downstream1 := startSpyLoggregatorV2Ingress()

		forwarderAgent = app.NewForwarderAgent(cfg, mc, testLogger)
		go forwarderAgent.Run()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()

		wg.Add(1)
		emitEnvelopes(ctx, 10*time.Millisecond, &wg)

		var e1 *loggregator_v2.Envelope
		Eventually(downstream1.envelopes, 5).Should(Receive(&e1))

		Expect(e1.GetTags()).To(HaveLen(1))
		Expect(e1.GetTags()["some-tag"]).To(Equal("some-value"))
	})

	It("continues writing to other consumers if one is slow", func() {
		downstreamNormal := startSpyLoggregatorV2Ingress()
		startSpyLoggregatorV2BlockingIngress()

		forwarderAgent = app.NewForwarderAgent(cfg, mc, testLogger)
		go forwarderAgent.Run()

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		defer wg.Wait()
		defer cancel()

		wg.Add(1)
		emitEnvelopes(ctx, 1*time.Millisecond, &wg)

		Eventually(downstreamNormal.envelopes, 5).Should(Receive())

		var prevSize int
		Consistently(func() bool {
			notEqual := len(downstreamNormal.envelopes) != prevSize
			prevSize = len(downstreamNormal.envelopes)
			return notEqual
		}, 5, 1).Should(BeTrue())
	})
})

var sampleEnvelope = &loggregator_v2.Envelope{
	Timestamp: time.Now().UnixNano(),
	SourceId:  "some-id",
	Message: &loggregator_v2.Envelope_Log{
		Log: &loggregator_v2.Log{
			Payload: []byte("hello"),
		},
	},
	Tags: map[string]string{
		"some-tag": "some-value",
	},
}

var sampleCounter = &loggregator_v2.Envelope{
	Timestamp: time.Now().UnixNano(),
	SourceId:  "some-id",
	Message: &loggregator_v2.Envelope_Counter{
		Counter: &loggregator_v2.Counter{
			Delta: 20,
			Total: 0,
		},
	},
}

func newIngressClient(port int) *loggregator.IngressClient {
	tlsConfig, err := loggregator.NewIngressTLSConfig(
		testhelper.Cert("loggregator-ca.crt"),
		testhelper.Cert("metron.crt"),
		testhelper.Cert("metron.key"),
	)
	Expect(err).ToNot(HaveOccurred())
	ingressClient, err := loggregator.NewIngressClient(
		tlsConfig,
		loggregator.WithAddr(fmt.Sprintf("127.0.0.1:%d", port)),
		loggregator.WithLogger(log.New(GinkgoWriter, "[TEST INGRESS CLIENT] ", 0)),
		loggregator.WithBatchMaxSize(1),
	)
	Expect(err).ToNot(HaveOccurred())
	return ingressClient
}

func startForwarderAgent(envs ...string) *gexec.Session {
	path, err := gexec.Build("code.cloudfoundry.org/loggregator-agent/cmd/forwarder-agent")
	if err != nil {
		panic(err)
	}

	cmd := exec.Command(path)
	cmd.Env = envs
	session, err := gexec.Start(cmd, GinkgoWriter, GinkgoWriter)
	if err != nil {
		panic(err)
	}

	return session
}

func startSpyLoggregatorV2Ingress() *spyLoggregatorV2Ingress {
	s := &spyLoggregatorV2Ingress{
		envelopes: make(chan *loggregator_v2.Envelope, 10000),
	}

	serverCreds, err := plumbing.NewServerCredentials(
		testhelper.Cert("metron.crt"),
		testhelper.Cert("metron.key"),
		testhelper.Cert("loggregator-ca.crt"),
	)

	lis, err := net.Listen("tcp", ":0")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	grpcServer := grpc.NewServer(grpc.Creds(serverCreds))
	loggregator_v2.RegisterIngressServer(grpcServer, s)

	s.close = func() {
		lis.Close()
	}
	s.addr = lis.Addr().String()
	port := strings.Split(s.addr, ":")

	createForwarderPortConfigFile(port[len(port)-1])
	go grpcServer.Serve(lis)

	return s
}

type spyLoggregatorV2Ingress struct {
	addr      string
	close     func()
	envelopes chan *loggregator_v2.Envelope
}

func (s *spyLoggregatorV2Ingress) Sender(loggregator_v2.Ingress_SenderServer) error {
	panic("not implemented")
}

func (s *spyLoggregatorV2Ingress) Send(context.Context, *loggregator_v2.EnvelopeBatch) (*loggregator_v2.SendResponse, error) {
	panic("not implemented")
}

func (s *spyLoggregatorV2Ingress) BatchSender(srv loggregator_v2.Ingress_BatchSenderServer) error {
	for {
		batch, err := srv.Recv()
		if err != nil {
			return err
		}

		for _, e := range batch.Batch {
			s.envelopes <- e
		}
	}
}

func startSpyLoggregatorV2BlockingIngress() *spyLoggregatorV2BlockingIngress {
	s := &spyLoggregatorV2BlockingIngress{}

	serverCreds, err := plumbing.NewServerCredentials(
		testhelper.Cert("metron.crt"),
		testhelper.Cert("metron.key"),
		testhelper.Cert("loggregator-ca.crt"),
	)

	lis, err := net.Listen("tcp", ":0")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())

	grpcServer := grpc.NewServer(grpc.Creds(serverCreds))
	loggregator_v2.RegisterIngressServer(grpcServer, s)

	s.close = func() {
		lis.Close()
	}
	s.addr = lis.Addr().String()

	port := strings.Split(s.addr, ":")
	createForwarderPortConfigFile(port[len(port)-1])
	go grpcServer.Serve(lis)

	return s
}

type spyLoggregatorV2BlockingIngress struct {
	addr  string
	close func()
}

func (s *spyLoggregatorV2BlockingIngress) Sender(loggregator_v2.Ingress_SenderServer) error {
	panic("not implemented")
}

func (s *spyLoggregatorV2BlockingIngress) Send(context.Context, *loggregator_v2.EnvelopeBatch) (*loggregator_v2.SendResponse, error) {
	panic("not implemented")
}

func (s *spyLoggregatorV2BlockingIngress) BatchSender(srv loggregator_v2.Ingress_BatchSenderServer) error {
	c := make(chan struct{})
	for {
		_, err := srv.Recv()
		if err != nil {
			return err
		}

		<-c
	}
}

func forwarderPortConfigDir() string {
	dir, err := ioutil.TempDir(".", "")
	if err != nil {
		log.Fatal(err)
	}

	return dir
}

func createForwarderPortConfigFile(port string) {
	fDir, err := ioutil.TempDir(fConfigDir, "")
	if err != nil {
		log.Fatal(err)
	}

	tmpfn := filepath.Join(fDir, "ingress_port.yml")
	tmpfn, err = filepath.Abs(tmpfn)
	Expect(err).ToNot(HaveOccurred())

	contents := fmt.Sprintf(forwardConfigTemplate, port)
	if err := ioutil.WriteFile(tmpfn, []byte(contents), 0666); err != nil {
		log.Fatal(err)
	}
}
