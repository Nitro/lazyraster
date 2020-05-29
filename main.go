package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Nitro/filecache"
	"github.com/kelseyhightower/envconfig"
	"github.com/relistan/rubberneck"
	log "github.com/sirupsen/logrus"
	"github.com/yvasiyarov/gorelic"
)

const (
	ShutdownTimeout    = 10 * time.Second
)

// Config contains the application configuration parameters
type Config struct {
	BaseDir                 string   `envconfig:"BASE_DIR" default:"."`
	HttpPort                int      `envconfig:"HTTP_PORT" default:"8000"`
	AdvertiseHttpPort       int      `envconfig:"ADVERTISE_HTTP_PORT" default:"8000"`
	AwsRegion               string   `envconfig:"AWS_REGION" default:"eu-central-1"`
	CacheSize               int      `envconfig:"CACHE_SIZE" default:"512"`
	UrlSigningSecret        string   `envconfig:"URL_SIGNING_SECRET" default:"deadbeef"`
	RasterCacheSize         int      `envconfig:"RASTER_CACHE_SIZE" default:"20"`
	RasterBufferSize        int      `envconfig:"RASTER_BUFFER_SIZE" default:"10"`
	LoggingLevel            string   `envconfig:"LOGGING_LEVEL" default:"info"`
}

func configureLoggingLevel(config *Config) {
	level := config.LoggingLevel
	switch {
	case len(level) == 0:
		log.SetLevel(log.InfoLevel)
	case level == "info":
		log.SetLevel(log.InfoLevel)
	case level == "warn":
		log.SetLevel(log.WarnLevel)
	case level == "error":
		log.SetLevel(log.ErrorLevel)
	case level == "debug":
		log.SetLevel(log.DebugLevel)
	}
}

// Set up some signal handling for term/int and try to exit the
// cluster and clean out the cache before we exit.
func handleSignals(fCache *filecache.FileCache) {
	sigChan := make(chan os.Signal, 1) // Buffered!

	// Grab some signals we want to catch where possible
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Warnf("Received signal '%s', attempting clean shutdown", sig)

	waitChan := make(chan struct{}, 1)
	fCache.PurgeAsync(waitChan)

	log.Infof("Clean shutdown initiated... could take up to %s", ShutdownTimeout)
	// Try waiting for the purge to complete, but don't wait forever
	select {
	case <-waitChan: // nothing
	case <-time.After(ShutdownTimeout): // nothing
	}

	os.Exit(130) // Ctrl-C received or equivalent
}

// configureNewRelic sets up and starts a Gorelic agent if we have a
// New Relic license available.
func configureNewRelic() *gorelic.Agent {
	nrLicense := os.Getenv("NEW_RELIC_LICENSE_KEY")
	var agent *gorelic.Agent
	if nrLicense == "" {
		log.Info("No New Relic license found, not starting an agent")

		return nil
	}

	log.Infof("Configuring New Relic agent (Gorelic) with license '%s'", nrLicense)

	agent = gorelic.NewAgent()
	svcName := os.Getenv("SERVICE_NAME")
	envName := os.Getenv("ENVIRONMENT_NAME")
	if svcName != "" && envName != "" {
		nrName := fmt.Sprintf("%s-%s", svcName, envName)
		log.Infof("Registering with New Relic app name: %s", nrName)
		agent.NewrelicName = nrName
	}
	agent.CollectHTTPStatuses = true
	agent.CollectHTTPStat = true
	agent.NewrelicLicense = nrLicense
	err := agent.Run()
	if err != nil {
		log.Errorf("Failed to start NewRelic agent: %s", err)
		return nil
	}

	return agent
}

func main() {
	var config Config

	err := envconfig.Process("raster", &config)
	if err != nil {
		log.Fatalf("Failed to parse the configuration parameters: %s", err)
	}

	configureLoggingLevel(&config)

	rubberneck.NewPrinter(log.Infof, rubberneck.NoAddLineFeed).Print(config)

	// New Relic
	agent := configureNewRelic()

	// Set up a rasterizer cache (in memory, keeps open documents ready to go)
	rasterCache, err := NewRasterCache(config.RasterCacheSize)
	if err != nil {
		log.Fatalf("Unable to initialize the rasterizer cache: %s", err)
	}

	// Set up a filecache to underly the rasterCache
	fCache, err := filecache.New(
		config.CacheSize,
		config.BaseDir,
		filecache.DownloadTimeout(ServerWriteTimeout),
		// If we get a document with no extension, assume PDF
		filecache.DefaultExtension(".pdf"),
		// Enable both S3 and Dropbox downloaders
		filecache.S3Downloader(config.AwsRegion),
		filecache.DropboxDownloader(),
	)
	if err != nil {
		log.Fatalf("Unable to create LRU cache: %s", err)
	}

	// Wrap the download function with Gorelic to report on download times
	if agent != nil {
		origFunc := fCache.DownloadFunc
		fCache.DownloadFunc = func(dr *filecache.DownloadRecord, localPath string) error {
			t := agent.Tracer.BeginTrace("fileFetch")
			defer t.EndTrace()
			return origFunc(dr, localPath)
		}
	}

	// Tie the deletion from file cache to the deletion from the rasterCache
	fCache.OnEvict = func(hashKey interface{}, filename interface{}) {
		// We need to make sure we delete a rasterizer if one exists and the file
		// has been deleted out from under it.
		rasterCache.Remove(filename.(string)) // Actual filename on disk
	}

	// Set up the signal handler to try to clean up on exit
	go handleSignals(fCache)

	err = serveHttp(&config, fCache, rasterCache, config.UrlSigningSecret, agent)
	if err != nil {
		log.Fatalf("Failed to start HTTP server: %s", err)
	}
}
