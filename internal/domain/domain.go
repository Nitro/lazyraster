package domain

import (
	"encoding/json"
	"fmt"
)

// ParseAnnotations decodes a JSON array of annotations into the concrete domain annotation types,
// discriminating on the "type" field. A nil/empty/JSON-null input yields an empty slice so callers
// can treat "no annotations" uniformly. It is the single source of truth for annotation decoding,
// shared by the Redis-backed legacy path and the inline request-body render path.
func ParseAnnotations(input []byte) ([]any, error) {
	if len(input) == 0 {
		return []any{}, nil
	}

	var rawEntries []json.RawMessage
	if err := json.Unmarshal(input, &rawEntries); err != nil {
		return nil, err
	}

	result := make([]any, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		discriminator := struct {
			Type string `json:"type"`
		}{}
		if err := json.Unmarshal(rawEntry, &discriminator); err != nil {
			return nil, fmt.Errorf("failed to unmarshal message: %w", err)
		}

		var (
			value any
			err   error
		)
		switch discriminator.Type {
		case "checkbox":
			value, err = decodeAnnotation[AnnotationCheckbox](rawEntry)
		case "image":
			value, err = decodeAnnotation[AnnotationImage](rawEntry)
		case "text":
			value, err = decodeAnnotation[AnnotationText](rawEntry)
		default:
			return nil, fmt.Errorf("unknown annotation type '%s'", discriminator.Type)
		}
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}

	return result, nil
}

func decodeAnnotation[T any](raw json.RawMessage) (any, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	return value, nil
}

type AnnotationLocation struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type AnnotationSize struct {
	Height float64 `json:"height"`
	Width  float64 `json:"width"`
}

type AnnotationTextFont struct {
	Family string  `json:"family"`
	Size   float64 `json:"size"`
}

type AnnotationImage struct {
	Page          int                `json:"page"`
	Location      AnnotationLocation `json:"location"`
	Size          AnnotationSize     `json:"size"`
	ImageLocation string             `json:"imageLocation"`
}

type AnnotationText struct {
	Value    string             `json:"value"`
	Page     int                `json:"page"`
	Location AnnotationLocation `json:"location"`
	Font     AnnotationTextFont `json:"font"`
	Size     AnnotationSize     `json:"size"`
}

type AnnotationCheckbox struct {
	Value    bool               `json:"value"`
	Page     int                `json:"page"`
	Location AnnotationLocation `json:"location"`
	Size     AnnotationSize     `json:"size"`
}
