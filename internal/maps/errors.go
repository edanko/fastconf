package maps

import (
	"errors"
	"fmt"
)

var errEmptyPath = errors.New("empty configuration path")

type notAnObjectError struct {
	key string
}

func (e *notAnObjectError) Error() string {
	return fmt.Sprintf("configuration key %q is not an object", e.key)
}
