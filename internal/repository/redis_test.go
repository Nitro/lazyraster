package repository

import (
	"testing"

	"github.com/nitro/lazyraster/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRedisClientParseAnnotations(t *testing.T) {
	payload := `
		[
			{"type":"checkbox","location":{"x":1.0,"y":1.0},"page":1,"size":{"height":1.0,"width":1.0},"value":true},
			{
				"type":"image",
				"imageLocation": "imageLocation",
				"location": {"x":2.0,"y":2.0},
				"page": 2,
				"size": {"height":2.0,"width":2.0}
			},
			{"type":"text","font":{"family":"font","size":3.0},"location":{"x":3.0,"y":3.0},"page":3,"value":"text"}
		]
	`
	expected := []any{
		domain.AnnotationCheckbox{
			Location: domain.AnnotationLocation{
				X: 1.0,
				Y: 1.0,
			},
			Page: 1,
			Size: domain.AnnotationSize{
				Height: 1.0,
				Width:  1.0,
			},
			Value: true,
		},
		domain.AnnotationImage{
			ImageLocation: "imageLocation",
			Page:          2,
			Location: domain.AnnotationLocation{
				X: 2.0,
				Y: 2.0,
			},
			Size: domain.AnnotationSize{
				Height: 2.0,
				Width:  2.0,
			},
		},
		domain.AnnotationText{
			Value: "text",
			Page:  3,
			Location: domain.AnnotationLocation{
				X: 3.0,
				Y: 3.0,
			},
			Font: domain.AnnotationTextFont{
				Family: "font",
				Size:   3,
			},
		},
	}

	var c RedisClient
	result, err := c.parseAnnotations(payload)
	require.NoError(t, err)
	require.Equal(t, expected, result)
}
