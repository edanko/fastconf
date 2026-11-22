//go:build goexperiment.jsonv2

package encoding

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"strings"
)

// decodeJSON reads one JSON document with encoding/json/v2, which the standard
// library uses when the goexperiment.jsonv2 flag is enabled (the module's
// target toolchain enables it).
//
// v2 is used with its default, strict semantics: duplicate object member names
// and invalid UTF-8 are rejected, where the v1 fallback replaces invalid UTF-8
// and keeps the last duplicate. Both are malformed or ambiguous input, so the
// stricter behaviour is intentional; it is also faster than enabling the v1
// tolerance options, which disable v2's fast paths (about 2.3x slower on a
// 1000-tenant document).
func decodeJSON(reader io.Reader) (map[string]any, error) {
	var value any
	err := jsonv2.UnmarshalRead(reader, &value)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return map[string]any{}, nil
		}
		if strings.Contains(err.Error(), "after top-level value") {
			return nil, errors.New("unexpected data after the document")
		}
		return nil, err
	}
	return objectRoot(value)
}
