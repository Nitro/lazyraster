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
	log "github.com/Sirupsen/logrus"
	"github.com/kelseyhightower/envconfig"
	"github.com/relistan/rubberneck"
)

// Config contains the application configuration parameters
type Config struct {
	BaseDir                 string   `envconfig:"BASE_DIR" default:"."`
	Port                    string   `envconfig:"PORT" default:"8000"`
	AwsRegion               string   `envconfig:"AWS_REGION" default:"us-west-1"`
	S3Bucket                string   `envconfig:"S3_BUCKET" default:"nitro-junk"`
	ClusterSeeds            []string `envconfig:"CLUSTER_SEEDS"`
	CacheSize               int      `envconfig:"CACHE_SIZE" default:"512"`
	RedisPort               int      `envconfig:"REDIS_PORT" default:"6379"`
	ClusterName             string   `envconfig:"CLUSTER_NAME" default:"default"`
	MemberlistAdvertiseAddr string   `envconfig:"MEMBERLIST_ADVERTISE_ADDR"`
	MemberlistAdvertisePort int      `envconfig:"MEMBERLIST_ADVERTISE_PORT" default:"7946"`
	// Change this to some other port when running on the same box as Sidecar
	MemberlistBindPort int `envconfig:"MEMBERLIST_BIND_PORT" default:"7946"`
}

func setMesosConfig(config *Config) {
	// The Memberlist AdvertiseAddr requires an IP address
	ipAddr, err := net.LookupIP(os.Getenv("MESOS_HOSTNAME"))
	if err == nil {
		// Use the first resolved IP and assume it's the one we want...
		config.MemberlistAdvertiseAddr = ipAddr[0].String()
	}

	// Try to fetch the port mapped by Mesos for the Memberlist bind port
	port, err := strconv.Atoi(os.Getenv("MESOS_PORT_" + strconv.Itoa(config.MemberlistBindPort)))
	if err == nil {
		config.MemberlistAdvertisePort = port
	}
}

func addDefaultClusterSeed(config *Config) {
	defaultClusterSeed := "127.0.0.1:" + strconv.Itoa(config.MemberlistBindPort)

	config.ClusterSeeds = append(config.ClusterSeeds, defaultClusterSeed)
}

// Set up some signal handling for kill/term/int and try to exit the
// cluster and clean out the cache before we exit.
func handleSignals(fCache *filecache.FileCache, mList *memberlist.Memberlist) {
	sigChan := make(chan os.Signal, 1) // Buffered!

	// Grab some signals we want to catch where possible
	signal.Notify(sigChan, os.Interrupt)
	signal.Notify(sigChan, os.Kill)
	signal.Notify(sigChan, syscall.SIGTERM)

	sig := <-sigChan
	log.Warnf("Received signal '%s', attempting clean shutdown", sig)
	mList.Leave(2 * time.Second) // 2 second timeout

	err := mList.Shutdown()
	if err != nil {
		log.Warnf("Got error while shutting down Memberlist: %s", err)
	}

	go fCache.Cache.Purge()

	log.Info("Clean shutdown initiated... waiting")
	time.Sleep(3 * time.Second) // Try to let it quit
	os.Exit(130)                // Ctrl-C received or equivalent
}

func main() {
	log.SetLevel(log.DebugLevel)

	var config Config

	err := envconfig.Process("raster", &config)
	if err != nil {
		log.Fatalf("Failed to parse the configuration parameters: %s", err)
	}

	setMesosConfig(&config)
	addDefaultClusterSeed(&config)

	rubberneck.NewPrinter(log.Infof, rubberneck.NoAddLineFeed).Print(config)

	mlConfig := memberlist.DefaultLANConfig()

	mlConfig.BindPort = config.MemberlistBindPort
	mlConfig.AdvertiseAddr = config.MemberlistAdvertiseAddr
	mlConfig.AdvertisePort = config.MemberlistAdvertisePort
	ring, err := ringman.NewMemberlistRing(
		mlConfig,
		config.ClusterSeeds, config.Port, config.ClusterName,
	)
	if err != nil {
		log.Fatalf("Unable to establish memberlist ring: %s", err)
	}

	rasterCache, err := NewDefaultRasterCache()
	if err != nil {
		log.Fatalf("Unable to initialize the rasterizer cache: %s", err)
	}

	fCache, err := filecache.NewS3Cache(512, config.BaseDir, config.S3Bucket, config.AwsRegion)
	if err != nil {
		log.Fatalf("Unable to create LRU cache: %s", err)
	}

	fCache.OnEvict = func(hashKey interface{}, filename interface{}) {
		// We need to make sure we delete a rasterizer if one exists and the file
		// has been deleted out from under it.
		rasterCache.Remove(filename.(string)) // Actual filename on disk
	}

	// Set up the signal handler to try to clean up on exit
	go handleSignals(fCache, ring.Memberlist)

	// Run the Redis protocol server and wire it up to our hash ring
	go func() {
		err := serveRedis(fmt.Sprintf(":%d", config.RedisPort), ring.Manager)
		if err != nil {
			log.Fatalf("Error starting Redis protocol server: %s", err)
		}
	}()

	err = serveHttp(&config, fCache, ring, rasterCache)
	if err != nil {
		panic(err.Error())
	}
}
