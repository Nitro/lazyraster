package domain

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
