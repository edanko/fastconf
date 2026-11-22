//go:build !goexperiment.jsonv2

package encoding

import (
	"encoding/json"
	"errors"
	"io"
)

// decodeJSON reads one JSON document with the standard library v1 decoder.
//
// The input must contain exactly one value and its root must be an object;
// trailing content after the value is reported as an error so that truncated
// or concatenated files are not accepted silently.
func decodeJSON(reader io.Reader) (map[string]any, error) {
	decoder := json.NewDecoder(reader)
	var value any
	if err := decoder.Decode(&value); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case err == nil:
		return nil, errors.New("unexpected data after the document")
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	return objectRoot(value)
}
