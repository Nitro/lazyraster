package main

import (
	"fmt"

	"github.com/Nitro/filecache"
	"github.com/Nitro/memberlist"
	"github.com/Nitro/ringman"
	log "github.com/Sirupsen/logrus"
	"github.com/kelseyhightower/envconfig"
	"github.com/relistan/rubberneck"
)

// Config contains the application configuration parameters
type Config struct {
	BaseDir      string   `split_words:"true" default:"."`
	Port         string   `split_words:"true" default:"8000"`
	AwsRegion    string   `split_words:"true" default:"us-west-1"`
	S3Bucket     string   `split_words:"true" default:"nitro-junk"`
	ClusterSeeds []string `split_words:"true" default:"127.0.0.1"`
	CacheSize    int      `split_words:"true" default:"512"`
	RedisPort    int      `split_words:"true" default:"6379"`
	ClusterName  string   `split_words:"true" default:"default"`
}

func main() {
	log.SetLevel(log.DebugLevel)

	var config Config

	err := envconfig.Process("raster", &config)
	if err != nil {
		log.Fatalf("Failed to parse the configuration parameters: %s", err)
	}

	rubberneck.NewPrinter(log.Infof, rubberneck.NoAddLineFeed).Print(config)

	ring, err := ringman.NewMemberlistRing(
		memberlist.DefaultLANConfig(),
		config.ClusterSeeds, config.Port, config.ClusterName,
	)
	if err != nil {
		log.Fatalf("Unble to establish memberlist ring: %s", err)
	}

	rasterCache, err := NewDefaultRasterCache()
	if err != nil {
		log.Fatalf("Unble to initialize the rasterizer cache: %s", err)
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

	// Run the Redis protocol server and wire it up to our hash ring
	go func() {
		errServeRedis := serveRedis(fmt.Sprintf(":%d", config.RedisPort), ring.Manager)
		if errServeRedis != nil {
			log.Fatalf("Error starting Redis protocol server: %s", errServeRedis)
		}
	}()

	err = serveHttp(&config, fCache, ring, rasterCache)
	if err != nil {
		panic(err.Error())
	}
}
