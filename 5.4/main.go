package main

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"

	"github.com/go-kit/kit/log"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"go.opentelemetry.io/otel/trace"
)

// global vars...gasp!
var addr = "localhost:8000"
var tracer trace.Tracer
var httpClient http.Client
var logger log.Logger

var metricRequestLatency = promauto.NewHistogram(prometheus.HistogramOpts{
	Namespace: "demo",
	Name:      "request_latency_seconds",
	Help:      "Request Latency",
	Buckets:   prometheus.ExponentialBuckets(.0001, 2, 50),
})

func main() {
	flush := initTracer()
	defer flush()

	// initiate globals
	tracer = otel.Tracer("demo-app")
	httpClient = http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)}
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)

	// create and start server
	server := instrumentedServer(handler)

	fmt.Println("listening...")
	server.ListenAndServe()
}

func handler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	longRunningProcess(ctx)

	// check cache
	if shouldExecute(40) {
		url := "http://" + addr + "/"

		resp, err := instrumentedGet(ctx, url)
		defer resp.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	// query database
	if shouldExecute(40) {
		url := "http://" + addr + "/"

		resp, err := instrumentedGet(ctx, url)
		defer resp.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func shouldExecute(percent int) bool {
	return rand.Int()%100 < percent
}

func longRunningProcess(ctx context.Context) {
	ctx, sp := tracer.Start(ctx, "Long Running Process")
	defer sp.End()

	time.Sleep(time.Millisecond * 50)
	sp.AddEvent("halfway done!")
	time.Sleep(time.Millisecond * 50)
}

/***
Tracing
***/
// Initializes an OTLP exporter, and configures the trace provider
func initTracer() func() {
	ctx := context.Background()
        fmt.Println("hello world 1")

	driver := otlpgrpc.NewDriver(
		otlpgrpc.WithInsecure(),
		otlpgrpc.WithEndpoint("localhost:4317"),
		otlpgrpc.WithDialOption(grpc.WithBlock()), // useful for testing
	)
	exp, err := otlp.NewExporter(ctx, driver)
	handleErr(err, "failed to create exporter")

	res, err := resource.New(ctx,
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceNameKey.String("demo-service"),
		),
	)
	handleErr(err, "failed to create resource")
        fmt.Println("hello world 2")

	bsp := sdktrace.NewBatchSpanProcessor(exp)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.AlwaysSample()}),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)

	// set global propagator to tracecontext (the default is no-op).
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(tracerProvider)

	return func() {
		// Shutdown will flush any remaining spans.
		handleErr(tracerProvider.Shutdown(ctx), "failed to shutdown TracerProvider")
	}
}

/***
Server
***/
func instrumentedServer(handler http.HandlerFunc) *http.Server {
	// OpenMetrics handler : metrics and exemplars
	omHandleFunc := func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
                fmt.Println("hello world")
		handler.ServeHTTP(w, r)
                fmt.Println("hello world")
		ctx := r.Context()
		traceID := trace.SpanContextFromContext(ctx).TraceID.String()

		metricRequestLatency.(prometheus.ExemplarObserver).ObserveWithExemplar(
			time.Since(start).Seconds(), prometheus.Labels{"traceID": traceID},
		)

		// log the trace id with other fields so we can discover traces through logs
		logger.Log("msg", "http request", "traceID", traceID, "path", r.URL.Path, "latency", time.Since(start))
	}

	// OTel handler : traces
	otelHandler := otelhttp.NewHandler(http.HandlerFunc(omHandleFunc), "http")

	r := mux.NewRouter()
	r.Handle("/", otelHandler)
	r.Handle("/metrics", promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	return &http.Server{
		Handler: r,
		Addr:    "0.0.0.0:8000",
	}
}

/***
Client
***/
func instrumentedGet(ctx context.Context, url string) (*http.Response, error) {
	// create http request
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		panic(err)
	}

	return httpClient.Do(req)
}

func handleErr(err error, message string) {
	if err != nil {
		panic(fmt.Sprintf("%s: %s", err, message))
	}
}
