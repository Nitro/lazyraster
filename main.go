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

const (
	ImageMaxWidth = 4096
)

type Config struct {
	BaseDir      string   `envconfig:"BASE_DIR" default:"."`
	Port         string   `envconfig:"PORT" default:"8000"`
	AwsRegion    string   `envconfig:"AWS_REGION" default:"us-west-1"`
	S3Bucket     string   `envconfig:"S3_BUCKET" default:"nitro-junk"`
	ClusterSeeds []string `envconfig:"CLUSTER_SEEDS" default:"127.0.0.1"`
	CacheSize    int      `envconfig:"CACHE_SIZE" default:"512"`
	RedisPort    int      `envconfig:"REDIS_PORT" default:"6379"`
	ClusterName  string   `envconfig""CLUSTER_NAME" default:"default"`
}

func main() {
	log.SetLevel(log.DebugLevel)

	var config Config

	envconfig.Process("raster", &config)
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
