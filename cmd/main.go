package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/nitro/lazyraster/internal"
)

func main() {
	var (
		logger           = zerolog.New(os.Stdout).With().Timestamp().Caller().Logger()
		cacheBucket      = os.Getenv("CACHE_BUCKET")
		cacheSecret      = os.Getenv("CACHE_SECRET")
		urlSigningSecret = os.Getenv("URL_SIGNING_SECRET")
		enableDatadog    = os.Getenv("ENABLE_DATADOG")
	)
	if cacheBucket == "" {
		logger.Fatal().Msg("Environment variable 'CACHE_BUCKET' can't be empty")
	}
	if cacheSecret == "" {
		logger.Fatal().Msg("Environment variable 'CACHE_SECRET' can't be empty")
	}
	if urlSigningSecret == "" {
		logger.Fatal().Msg("Environment variable 'URL_SIGNING_SECRET' can't be empty")
	}

	waitHandlerAsyncError, waitHandler := wait(logger)
	client := internal.Client{
		Logger:            logger,
		AsyncErrorHandler: waitHandlerAsyncError,
		CacheBucket:       cacheBucket,
		CacheSecret:       cacheSecret,
		URLSigningSecret:  urlSigningSecret,
		EnableDatadog:     enableDatadog == "true",
	}
	if err := client.Init(); err != nil {
		logger.Fatal().Err(err).Msg("Fail to initialize the client")
	}
	client.Start()

	exitStatus := waitHandler()
	ctx, ctxCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := client.Stop(ctx); err != nil {
		ctxCancel()
		logger.Fatal().Err(err).Msg("Fail to stop the client")
	}
	ctxCancel()
	os.Exit(exitStatus)
}

func wait(logger zerolog.Logger) (func(error), func() int) {
	signalChan := make(chan os.Signal, 2)
	var exitStatus int32
	asyncError := func(err error) {
		logger.Error().Err(err).Msg("Async error happened")
		signalChan <- os.Interrupt
		atomic.AddInt32(&exitStatus, 1)
	}
	handler := func() int {
		signal.Notify(signalChan, os.Interrupt)
		<-signalChan
		return (int)(exitStatus)
	}
	return asyncError, handler
}
