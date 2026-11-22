package encoding

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// decodeJSON reads one JSON document.
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

// decodeYAML reads one YAML document.
//
// A YAML stream may hold several documents separated by "---"; configuration
// files hold exactly one, so additional non-empty documents are rejected
// instead of being ignored. Trailing empty documents are accepted.
func decodeYAML(reader io.Reader) (map[string]any, error) {
	decoder := yaml.NewDecoder(reader)
	var value any
	if err := decoder.Decode(&value); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	for {
		var extra any
		err := decoder.Decode(&extra)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if extra != nil {
			return nil, errors.New("multiple YAML documents per file are not supported")
		}
	}
	return objectRoot(value)
}

// decodeTOML reads one TOML document. TOML tables are always objects, so the
// root is an object unless the input is empty.
func decodeTOML(reader io.Reader) (map[string]any, error) {
	var value any
	if _, err := toml.NewDecoder(reader).Decode(&value); err != nil {
		return nil, err
	}
	return objectRoot(value)
}
