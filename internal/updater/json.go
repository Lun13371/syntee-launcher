package updater

import (
	"encoding/json"
	"io"
)

// decodeJSON reads a JSON body from r into v. We use json.NewDecoder so we
// don't have to buffer the whole body in memory — manifests are tiny but
// this keeps the path uniform with the rest of the codebase.
func decodeJSON(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	return dec.Decode(v)
}