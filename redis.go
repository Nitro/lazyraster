package main

import (
	"errors"
	"strings"
	"time"

	"github.com/Nitro/ringman"
	log "github.com/sirupsen/logrus"
	"github.com/bsm/redeo"
)

func measureSince(label string, startTime time.Time) {
	log.Debugf("%s: %s", label, time.Since(startTime))
}

// serveRedis runs the Redis protocol server
func serveRedis(addr string, ringman *ringman.HashRingManager) error {
	if !strings.Contains(addr, ":") {
		return errors.New("serveRedis(): Invalid address supplied. Must be of form 'addr:port' or ':port'")
	}

	if ringman == nil {
		return errors.New("serveRedis(): HashRingManager was nil")
	}

	srv := redeo.NewServer(&redeo.Config{Addr: addr})

	srv.HandleFunc("ping", func(out *redeo.Responder, _ *redeo.Request) error {
		out.WriteInlineString("PONG")
		return nil
	})

	srv.HandleFunc("info", func(out *redeo.Responder, _ *redeo.Request) error {
		out.WriteString(srv.Info().String())
		return nil
	})

	srv.HandleFunc("select", func(out *redeo.Responder, _ *redeo.Request) error {
		defer measureSince("select", time.Now().UTC())

		out.WriteOK()
		return nil
	})

	srv.HandleFunc("get", func(out *redeo.Responder, req *redeo.Request) error {
		defer measureSince("get", time.Now())

		if len(req.Args) != 1 {
			return req.WrongNumberOfArgs()
		}
		node, err := ringman.GetNode(req.Args[0])
		if err != nil {
			log.Errorf("Error fetching key '%s': %s", req.Args[0], err)
			return err
		}

		out.WriteString(node)
		return nil
	})

	srv.HandleFunc("client", func(out *redeo.Responder, req *redeo.Request) error {
		if len(req.Args) != 1 {
			return req.WrongNumberOfArgs()
		}

		switch req.Args[0] {
		case "list":
			out.WriteString(srv.Info().ClientsString())
		default:
			return req.UnknownCommand()
		}
		return nil
	})

	log.Infof("Listening on tcp://%s", srv.Addr())

	return srv.ListenAndServe()
}
