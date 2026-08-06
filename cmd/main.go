package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/nitro/lazyraster/v2/internal"
)

// shutdownTimeout bounds the graceful shutdown that a termination signal triggers. It has to exceed
// the router's own 15s request timeout (internal/transport.Server.initMiddleware) so an in-flight
// render finishes on its own terms instead of tripping this deadline and turning a clean stop into a
// Fatal, and it has to stay comfortably inside the pod's terminationGracePeriodSeconds so kubelet
// does not SIGKILL us part-way through.
const shutdownTimeout = 20 * time.Second

func main() {
	var (
		logger                 = configureLogger()
		urlSigningSecret       = os.Getenv("URL_SIGNING_SECRET")
		enableDatadog          = os.Getenv("ENABLE_DATADOG")
		rawStorageBucketRegion = os.Getenv("STORAGE_BUCKET_REGION")
	)
	if urlSigningSecret == "" {
		logger.Fatal().Msg("Environment variable 'URL_SIGNING_SECRET' can't be empty")
	}
	if rawStorageBucketRegion == "" {
		logger.Fatal().Msg("Environment variable 'STORAGE_BUCKET_REGION' can't be empty")
	}

	storageBucketRegion, err := parseStorageBucketRegion(rawStorageBucketRegion)
	if err != nil {
		logger.Fatal().Msg("Fail to parse the environment variable 'STORAGE_BUCKET_REGION' payload")
	}

	waitHandlerAsyncError, waitHandler := wait(logger)
	client := internal.Client{
		Logger:              logger,
		AsyncErrorHandler:   waitHandlerAsyncError,
		URLSigningSecret:    urlSigningSecret,
		EnableDatadog:       enableDatadog == "true",
		StorageBucketRegion: storageBucketRegion,
		RedisURL:            os.Getenv("REDIS_URL"),
		RedisUsername:       os.Getenv("REDIS_USERNAME"),
		RedisPassword:       os.Getenv("REDIS_PASSWORD"),
	}
	if err := client.Init(); err != nil {
		logger.Fatal().Err(err).Msg("Fail to initialize the client")
	}
	client.Start()

	exitStatus := waitHandler()
	ctx, ctxCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	if err := client.Stop(ctx); err != nil {
		ctxCancel()
		logger.Fatal().Err(err).Msg("Fail to stop the client")
	}
	ctxCancel()
	os.Exit(exitStatus)
}

func wait(logger zerolog.Logger) (func(error), func() int) {
	signalChan := make(chan os.Signal, 2)

	// SIGTERM is what Kubernetes and `docker stop` send to ask for a shutdown; SIGINT covers a local
	// Ctrl-C. Both must be registered: a signal absent from this list keeps its default disposition,
	// and for SIGTERM that means the runtime kills the process outright, so the handler below never
	// returns and main never reaches client.Stop. In-flight connections are then severed rather than
	// drained, which callers' Envoy sidecars report as UC and turn into 503s.
	//
	// Registering here rather than inside the handler also covers the window between process start
	// and the caller invoking the handler, during which the client is already accepting requests.
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	var exitStatus int32
	asyncError := func(err error) {
		logger.Error().Err(err).Msg("Async error happened")
		// Record the failure before waking the handler: it reads exitStatus as soon as it receives,
		// so incrementing afterwards races and can report a clean exit for a crash.
		atomic.AddInt32(&exitStatus, 1)
		signalChan <- os.Interrupt
	}
	handler := func() int {
		<-signalChan
		return int(atomic.LoadInt32(&exitStatus))
	}
	return asyncError, handler
}

func parseStorageBucketRegion(payload string) (map[string]string, error) {
	result := make(map[string]string)
	for _, segment := range strings.Split(payload, ";") {
		fragments := strings.Split(segment, ":")
		if len(fragments) != 2 {
			return nil, errors.New("invalid payload")
		}

		region := strings.TrimSpace(fragments[0])
		buckets := strings.Split(fragments[1], ",")
		if len(buckets) == 0 {
			return nil, errors.New("expected at least one bucket")
		}
		for _, bucket := range buckets {
			result[strings.TrimSpace(bucket)] = region
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("fail to parse the storage bucket region")
	}
	return result, nil
}

func configureLogger() zerolog.Logger {
	var logLevel zerolog.Level
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		logLevel = zerolog.DebugLevel
	case "info":
		logLevel = zerolog.InfoLevel
	case "warn":
		logLevel = zerolog.WarnLevel
	case "error":
		logLevel = zerolog.ErrorLevel
	default:
		logLevel = zerolog.InfoLevel
	}

	return zerolog.New(os.Stdout).With().Timestamp().Caller().Logger().Level(logLevel)
}
