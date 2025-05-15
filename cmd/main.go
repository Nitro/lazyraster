package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/nitro/lazyraster/v2/internal"
)

func main() {
	var (
		logger                 = zerolog.New(os.Stdout).With().Timestamp().Caller().Logger().Level(zerolog.InfoLevel)
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
