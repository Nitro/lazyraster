package domain

type AnnotationLocation struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type AnnotationSize struct {
	Height int `json:"height"`
	Width  int `json:"width"`
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
	Font     struct {
		Family string `json:"family"`
		Size   int    `json:"size"`
	} `json:"font"`
}

type AnnotationCheckbox struct {
	Value    bool               `json:"value"`
	Page     int                `json:"page"`
	Location AnnotationLocation `json:"location"`
	Size     AnnotationSize     `json:"size"`
}
