package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

var errUnsupportedJSONMediaType = errors.New("Content-Type must be application/json")

func decodeStrictJSON(ginContext *gin.Context, maximumBodyBytes int64, destination any) error {
	contentTypes := ginContext.Request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return errUnsupportedJSONMediaType
	}
	mediaType, _, mediaTypeError := mime.ParseMediaType(contentTypes[0])
	if mediaTypeError != nil || !strings.EqualFold(mediaType, "application/json") {
		return errUnsupportedJSONMediaType
	}
	ginContext.Request.Body = http.MaxBytesReader(ginContext.Writer, ginContext.Request.Body, maximumBodyBytes)
	decoder := json.NewDecoder(ginContext.Request.Body)
	decoder.DisallowUnknownFields()
	if decodeError := decoder.Decode(destination); decodeError != nil {
		return fmt.Errorf("invalid JSON body: %w", decodeError)
	}
	if trailingJSONError := decoder.Decode(&struct{}{}); !errors.Is(trailingJSONError, io.EOF) {
		return errors.New("JSON body must contain one object")
	}
	return nil
}

func strictJSONErrorStatus(decodeError error) int {
	var maximumBytesError *http.MaxBytesError
	switch {
	case errors.Is(decodeError, errUnsupportedJSONMediaType):
		return http.StatusUnsupportedMediaType
	case errors.As(decodeError, &maximumBytesError):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}
