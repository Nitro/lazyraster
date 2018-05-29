package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Nitro/filecache"
	"github.com/Nitro/memberlist"
	"github.com/Nitro/ringman"
	_ "github.com/ianlancetaylor/cgosymbolizer"
	"github.com/kelseyhightower/envconfig"
	"github.com/relistan/rubberneck"
	log "github.com/sirupsen/logrus"
	"github.com/yvasiyarov/gorelic"
)

const (
	ShutdownTimeout    = 10 * time.Second
	MemberlistBindPort = 7946
)

// Config contains the application configuration parameters
type Config struct {
	BaseDir                 string   `envconfig:"BASE_DIR" default:"."`
	HttpPort                int      `envconfig:"HTTP_PORT" default:"8000"`
	AdvertiseHttpPort       int      `envconfig:"ADVERTISE_HTTP_PORT" default:"8000"`
	AwsRegion               string   `envconfig:"AWS_REGION" default:"us-west-1"`
	ClusterSeeds            []string `envconfig:"CLUSTER_SEEDS"`
	CacheSize               int      `envconfig:"CACHE_SIZE" default:"512"`
	RedisPort               int      `envconfig:"REDIS_PORT" default:"6379"`
	ClusterName             string   `envconfig:"CLUSTER_NAME" default:"default"`
	RingType                string   `envconfig:"RING_TYPE" default:"sidecar"`
	AdvertiseMemberlistHost string   `envconfig:"ADVERTISE_MEMBERLIST_HOST"`
	AdvertiseMemberlistPort int      `envconfig:"ADVERTISE_MEMBERLIST_PORT" default:"7946"`
	SidecarUrl              string   `envconfig:"SIDECAR_URL" default:"http://192.168.168.168:7777/api/state.json"`
	SidecarServiceName      string   `envconfig:"SIDECAR_SERVICE_NAME" default:"lazyraster"`
	SidecarServicePort      int64    `envconfig:"SIDECAR_SERVICE_PORT" default:"10110"`
	UrlSigningSecret        string   `envconfig:"URL_SIGNING_SECRET"`
	RasterCacheSize         int      `envconfig:"RASTER_CACHE_SIZE" default:"20"`
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

func findMesosOverrideFor(port int, defaultPort int) (int, error) {
	if mesosPort, ok := os.LookupEnv("MESOS_PORT_" + strconv.Itoa(port)); ok {
		p, err := strconv.Atoi(mesosPort)
		if err != nil {
			return 0, fmt.Errorf("failed to parse the Mesos mapped port for '%d': %s", port, err)
		}

		return p, nil
	}

	// If we can't find a corresponding Mesos variable, return the input port
	return defaultPort, nil
}

func configureMesosMappings(config *Config) error {
	if hostname, ok := os.LookupEnv("MESOS_HOSTNAME"); ok {
		// The Memberlist AdvertiseAddr requires an IP address
		ipAddr, err := net.LookupIP(hostname)
		if err != nil {
			return fmt.Errorf("Failed to resolve the Mesos hostname '%s' IP: %s", hostname, err)
		}

		// Use the first resolved IP and assume it's the one we want...
		config.AdvertiseMemberlistHost = ipAddr[0].String()
	}

	// Try to fetch the port mapped by Mesos for the Lazyraster HTTP bind port
	// This port will be stored Memberlist
	var err error
	config.AdvertiseHttpPort, err =
		findMesosOverrideFor(config.HttpPort, config.AdvertiseHttpPort)
	if err != nil {
		return err
	}

	// Try to fetch the port mapped by Mesos for the Memberlist bind port
	config.AdvertiseMemberlistPort, err =
		findMesosOverrideFor(MemberlistBindPort, config.AdvertiseMemberlistPort)
	if err != nil {
		return err
	}

	return nil
}

// Set up some signal handling for kill/term/int and try to exit the
// cluster and clean out the cache before we exit.
func handleSignals(fCache *filecache.FileCache, ring ringman.Ring) {
	sigChan := make(chan os.Signal, 1) // Buffered!

	// Grab some signals we want to catch where possible
	signal.Notify(sigChan, os.Interrupt)
	signal.Notify(sigChan, os.Kill)
	signal.Notify(sigChan, syscall.SIGTERM)

	sig := <-sigChan
	log.Warnf("Received signal '%s', attempting clean shutdown", sig)

	// Stop the hashring and memberlist
	ring.Shutdown()

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
	agent.Run()

	return agent
}

// configureRing configures a ringman implementation using the right
// setup based on config.RingType
func configureRing(config *Config) (ringman.Ring, error) {
	switch config.RingType {
	case "memberlist":
		mlConfig := memberlist.DefaultLANConfig()

		mlConfig.BindPort = MemberlistBindPort
		mlConfig.AdvertiseAddr = config.AdvertiseMemberlistHost
		mlConfig.AdvertisePort = config.AdvertiseMemberlistPort
		if config.AdvertiseMemberlistHost != "" {
			mlConfig.Name = config.AdvertiseMemberlistHost
		}

		return ringman.NewMemberlistRing(
			mlConfig,
			config.ClusterSeeds, strconv.Itoa(config.AdvertiseHttpPort), config.ClusterName,
		)
	case "sidecar":
		return ringman.NewSidecarRing(
			config.SidecarUrl, config.SidecarServiceName, config.SidecarServicePort,
		)
	default:
		return nil, fmt.Errorf("Unknown RingType '%s'", config.RingType)
	}
}

func main() {
	var config Config

	err := envconfig.Process("raster", &config)
	if err != nil {
		log.Fatalf("Failed to parse the configuration parameters: %s", err)
	}

	configureLoggingLevel(&config)

	err = configureMesosMappings(&config)
	if err != nil {
		log.Fatalf("Failed set the Mesos config: %s", err)
	}

	rubberneck.NewPrinter(log.Infof, rubberneck.NoAddLineFeed).Print(config)

	// New Relic
	agent := configureNewRelic()

	var ring ringman.Ring
	ring, err = configureRing(&config)
	if err != nil {
		log.Fatalf("Unable to establish hashring ring ('%s': %s", config.RingType, err)
	}

	// Set up a rasterizer cache (in memory, keeps open documents ready to go)
	rasterCache, err := NewRasterCache(config.RasterCacheSize)
	if err != nil {
		log.Fatalf("Unable to initialize the rasterizer cache: %s", err)
	}

	// Set up an S3-backed filecache to underly the rasterCache
	fCache, err := filecache.NewS3Cache(
		config.CacheSize, config.BaseDir, config.AwsRegion, ServerWriteTimeout,
	)
	if err != nil {
		log.Fatalf("Unable to create LRU cache: %s", err)
	}

	// If we get a document from S3 with no extension, assume PDF
	fCache.DefaultExtension = ".pdf"

	// Wrap the S3 download function with Gorelic to report on S3 times
	if agent != nil {
		origFunc := fCache.DownloadFunc
		fCache.DownloadFunc = func(fname string, localPath string) error {
			t := agent.Tracer.BeginTrace("s3Fetch")
			defer t.EndTrace()
			return origFunc(fname, localPath)
		}
	}

	// Tie the deletion from file cache to the deletion from the rasterCache
	fCache.OnEvict = func(hashKey interface{}, filename interface{}) {
		// We need to make sure we delete a rasterizer if one exists and the file
		// has been deleted out from under it.
		rasterCache.Remove(filename.(string)) // Actual filename on disk
	}

	// Set up the signal handler to try to clean up on exit
	go handleSignals(fCache, ring)

	// Run the Redis protocol server and wire it up to our hash ring
	go func() {
		err := serveRedis(fmt.Sprintf(":%d", config.RedisPort), ring.Manager(), agent)
		if err != nil {
			log.Fatalf("Error starting Redis protocol server: %s", err)
		}
	}()

	err = serveHttp(&config, fCache, ring, rasterCache, config.UrlSigningSecret, agent)
	if err != nil {
		panic(err.Error())
	}
}
