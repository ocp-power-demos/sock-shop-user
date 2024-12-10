package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	corelog "log"

	"github.com/go-kit/kit/log"
	kitprometheus "github.com/go-kit/kit/metrics/prometheus"
	"github.com/microservices-demo/user/api"
	"github.com/microservices-demo/user/db"
	"github.com/microservices-demo/user/db/mongodb"
	opentracing "github.com/opentracing/opentracing-go"
	zipkinot "github.com/openzipkin-contrib/zipkin-go-opentracing"
	zgo "github.com/openzipkin/zipkin-go"
	zipkinhttp "github.com/openzipkin/zipkin-go/reporter/http"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	commonMiddleware "github.com/weaveworks/common/middleware"
)

var (
	port string
	zip  string
)

var (
	HTTPLatency = stdprometheus.NewHistogramVec(stdprometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Time (in seconds) spent serving HTTP requests.",
		Buckets: stdprometheus.DefBuckets,
	}, []string{"method", "path", "status_code", "isWS"})
)

const (
	ServiceName = "user"
)

func init() {
	stdprometheus.MustRegister(HTTPLatency)
	flag.StringVar(&zip, "zipkin", os.Getenv("ZIPKIN"), "Zipkin address")
	flag.StringVar(&port, "port", "8084", "Port on which to run")
	db.Register("mongodb", &mongodb.Mongo{})
}

func main() {

	flag.Parse()
	// Mechanical stuff.
	errc := make(chan error)

	// Log domain.
	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(os.Stderr)
		logger = log.With(logger, "ts", log.DefaultTimestampUTC)
		logger = log.With(logger, "caller", log.DefaultCaller)
	}

	// Find service local IP.
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		logger.Log("err", err)
		os.Exit(1)
	}
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	host := strings.Split(localAddr.String(), ":")[0]
	defer conn.Close()

	var tracer opentracing.Tracer
	{
		if zip != "" {
			logger := log.With(logger, "tracer", "Zipkin")
			logger.Log("addr", zip)

			reporter := zipkinhttp.NewReporter(zip)
			defer reporter.Close()

			// create our local service endpoint
			endpoint, err := zgo.NewEndpoint("myService", fmt.Sprintf("%v:%v", host, port))
			if err != nil {
				logger.Log("unable to create local endpoint", err)
				os.Exit(1)
			}

			// initialize our tracer
			t, err := zgo.NewTracer(reporter, zgo.WithLocalEndpoint(endpoint))
			if err != nil {
				logger.Log("tracer err", err)
				os.Exit(1)
			}

			// use zipkin-go-opentracing to wrap our tracer
			tracer := zipkinot.Wrap(t)

			// optionally set as Global OpenTracing tracer instance
			opentracing.SetGlobalTracer(tracer)
		} else {
			tracer = opentracing.NoopTracer{}
		}
	}

	dbconn := false
	for !dbconn {
		err := db.Init()
		if err != nil {
			if err == db.ErrNoDatabaseSelected {
				corelog.Fatal(err)
			}
			corelog.Print(err)
		} else {
			dbconn = true
		}
	}

	fieldKeys := []string{"method"}
	// Service domain.
	var service api.Service
	{
		service = api.NewFixedService()
		service = api.LoggingMiddleware(logger)(service)
		service = api.NewInstrumentingService(
			kitprometheus.NewCounterFrom(
				stdprometheus.CounterOpts{
					Namespace: "microservices_demo",
					Subsystem: "user",
					Name:      "request_count",
					Help:      "Number of requests received.",
				},
				fieldKeys),
			kitprometheus.NewSummaryFrom(stdprometheus.SummaryOpts{
				Namespace: "microservices_demo",
				Subsystem: "user",
				Name:      "request_latency_microseconds",
				Help:      "Total duration of requests in microseconds.",
			}, fieldKeys),
			service,
		)
	}

	// Endpoint domain.
	endpoints := api.MakeEndpoints(service, tracer)

	// HTTP router
	router := api.MakeHTTPHandler(endpoints, logger, tracer)

	httpMiddleware := []commonMiddleware.Interface{
		commonMiddleware.Instrument{
			Duration:     HTTPLatency,
			RouteMatcher: router,
		},
	}

	// Handler
	handler := commonMiddleware.Merge(httpMiddleware...).Wrap(router)

	// Create and launch the HTTP server.
	go func() {
		logger.Log("transport", "HTTP", "port", port)
		errc <- http.ListenAndServe(fmt.Sprintf(":%v", port), handler)
	}()

	// Capture interrupts.
	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errc <- fmt.Errorf("%s", <-c)
	}()

	logger.Log("exit", <-errc)
}
