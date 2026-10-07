// Package drf reproduces Django REST Framework's request parsing and
// serializer field validation: which inputs each field type accepts, how it
// coerces them, and the exact error messages it returns.
package drf

import (
	"io"
	"mime"
	"net/http"
	"net/url"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"

	"plane-lite/server/internal/httpx"
)

const maxBodyBytes = 10 << 20

// Value is one member of request.data: a JSON value, or a form string.
type Value struct {
	raw  jsontext.Value
	form bool // from a urlencoded/multipart body (DRF "html input")
}

// Kind is the JSON kind: 'n', 'f', 't', '"', '0', '{' or '['.
func (v Value) Kind() jsontext.Kind { return v.raw.Kind() }

func (v Value) IsNull() bool   { return v.Kind() == 'n' }
func (v Value) IsString() bool { return v.Kind() == '"' }

// Str returns the value of a JSON string.
func (v Value) Str() string {
	var s string
	_ = json.Unmarshal(v.raw, &s)
	return s
}

// Raw returns the JSON encoding of the value.
func (v Value) Raw() jsontext.Value { return v.raw }

// Data is request.data.
type Data struct {
	fields map[string]Value
	// typeName is the Python type of a JSON body that isn't an object
	// ("list", "str", ...); empty for dicts and form bodies.
	typeName string
}

// Parse reads the body the way DRF's JSONParser, FormParser and
// MultiPartParser do.
func Parse(r *http.Request) (*Data, error) {
	d := &Data{fields: map[string]Value{}}
	ct := r.Header.Get("Content-Type")
	mt, _, _ := mime.ParseMediaType(ct)
	switch {
	case r.Body == nil || r.ContentLength == 0 || ct == "":
		// DRF ignores bodies without a media type.
		return d, nil
	case mt == "application/json":
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			return nil, err
		}
		if len(body) == 0 {
			return d, nil
		}
		val := jsontext.Value(body)
		if err := val.Compact(jsontext.AllowDuplicateNames(true)); err != nil {
			return nil, httpx.Detail(http.StatusBadRequest, "JSON parse error - "+err.Error())
		}
		if val.Kind() != '{' {
			d.typeName = pyTypeName(val.Kind(), val)
			return d, nil
		}
		var members map[string]jsontext.Value
		if err := json.Unmarshal(val, &members, jsontext.AllowDuplicateNames(true)); err != nil {
			return nil, httpx.Detail(http.StatusBadRequest, "JSON parse error - "+err.Error())
		}
		for k, m := range members {
			d.fields[k] = Value{raw: m}
		}
		return d, nil
	case mt == "application/x-www-form-urlencoded", mt == "multipart/form-data":
		var form url.Values
		if mt == "multipart/form-data" {
			if err := r.ParseMultipartForm(maxBodyBytes); err != nil {
				return nil, httpx.Detail(http.StatusBadRequest, "Multipart form parse error - "+err.Error())
			}
			form = r.PostForm
		} else {
			if err := r.ParseForm(); err != nil {
				return nil, err
			}
			form = r.PostForm
		}
		for k, vs := range form {
			// QueryDict.__getitem__ returns the last value.
			raw, _ := json.Marshal(vs[len(vs)-1])
			d.fields[k] = Value{raw: raw, form: true}
		}
		return d, nil
	default:
		return nil, httpx.Detail(http.StatusUnsupportedMediaType, `Unsupported media type "`+ct+`" in request.`)
	}
}

// IsDict reports whether request.data is a mapping. Views that call
// request.data.get() on anything else crash with a 500.
func (d *Data) IsDict() bool { return d.typeName == "" }

// Get is request.data.get(name).
func (d *Data) Get(name string) (Value, bool) {
	v, ok := d.fields[name]
	return v, ok
}

// Has reports whether the key is present.
func (d *Data) Has(name string) bool {
	_, ok := d.fields[name]
	return ok
}

func pyTypeName(k jsontext.Kind, v jsontext.Value) string {
	switch k {
	case '[':
		return "list"
	case '"':
		return "str"
	case 'n':
		return "NoneType"
	case 't', 'f':
		return "bool"
	case '0':
		if isIntLiteral(string(v)) {
			return "int"
		}
		return "float"
	}
	return "dict"
}
