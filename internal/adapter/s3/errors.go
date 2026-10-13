package s3

import (
	"errors"

	"github.com/minio/minio-go/v7"
)

type notFoundError struct {
	key string
}

func (e *notFoundError) Error() string {
	return "s3: object not found: " + e.key
}

func errNotFound(key string) error {
	return &notFoundError{key: key}
}

// IsNotFound reports whether err means a missing object, unwrapping the
// minio ErrorResponse and the local not-found error alike.
func IsNotFound(err error) bool {
	return isNotFound(err)
}

func isNotFound(err error) bool {
	var notFound *notFoundError
	if errors.As(err, &notFound) {
		return true
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == 404 || resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket"
	}
	return false
}
