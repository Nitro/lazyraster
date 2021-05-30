package transport

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog"
)

const (
	maxBodySize = 100000 // 100kb.
)

type traceExtractor func(context.Context, zerolog.Logger) (zerolog.Logger, error)

type writer struct {
	logger         zerolog.Logger
	traceExtractor traceExtractor
}

func (wrt writer) response(ctx context.Context, w http.ResponseWriter, r interface{}, status int) {
	logger, err := wrt.traceExtractor(ctx, wrt.logger)
	if err != nil {
		logger.Err(err).Msg("Fail to extract the tracing ids")
		return
	}

	if r == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	content, err := json.Marshal(r)
	if err != nil {
		logger.Err(err).Msg("Fail to marshal the response")
		return
	}

	writed, err := w.Write(content)
	if err != nil {
		logger.Err(err).Msg("Fail to write the payload")
		return
	}
	if writed != len(content) {
		logger.Error().Msgf("Invalid quantity of writed bytes, expected %d and got %d", len(content), writed)
	}
}

// Error is used to generate a proper error content to be sent to the client.
func (wrt writer) error(ctx context.Context, w http.ResponseWriter, title string, err error, status int) {
	resp := struct {
		Error struct {
			Title  string `json:"title"`
			Detail string `json:"detail,omitempty"`
		} `json:"error"`
	}{}
	resp.Error.Title = title
	if err != nil {
		resp.Error.Detail = err.Error()
	}
	wrt.response(ctx, w, &resp, status)
}
