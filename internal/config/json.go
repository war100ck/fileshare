package config

import "encoding/json"

func unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func marshalIndent(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
