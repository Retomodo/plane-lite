// Package drf reproduces Django REST Framework's request parsing and
// serializer field validation: which inputs each field type accepts, how it
// coerces them, and the exact error messages it returns.
package drf

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"

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
	root   Value // the whole JSON body; an empty object for form or empty bodies
	fields map[string]Value
	keys   []string // dict order: first occurrence of each name
	// typeName is the Python type of a JSON body that isn't an object
	// ("list", "str", ...); empty for dicts and form bodies.
	typeName string
}

// Parse reads the body the way DRF's JSONParser, FormParser and
// MultiPartParser do.
func Parse(r *http.Request) (*Data, error) {
	d := &Data{root: Value{raw: jsontext.Value("{}")}, fields: map[string]Value{}}
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
		d.root = Value{raw: val}
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
		d.keys = objectKeys(val)
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
			d.keys = append(d.keys, k)
		}
		slices.Sort(d.keys) // url.Values keeps no order
		return d, nil
	default:
		return nil, httpx.Detail(http.StatusUnsupportedMediaType, `Unsupported media type "`+ct+`" in request.`)
	}
}

// Root is the whole of request.data (an empty dict for form bodies).
func (d *Data) Root() Value { return d.root }

// IsDict reports whether request.data is a mapping. Views that call
// request.data.get() on anything else crash with a 500.
func (d *Data) IsDict() bool { return d.typeName == "" }

// Get is request.data.get(name).
func (d *Data) Get(name string) (Value, bool) {
	v, ok := d.fields[name]
	return v, ok
}

// Set adds or replaces a member, as views building {**request.data, name: v}
// for a serializer do.
func (d *Data) Set(name string, v Value) {
	if _, ok := d.fields[name]; !ok {
		d.keys = append(d.keys, name)
	}
	d.fields[name] = v
}

// Delete is request.data.pop(name).
func (d *Data) Delete(name string) {
	if _, ok := d.fields[name]; !ok {
		return
	}
	delete(d.fields, name)
	d.keys = slices.DeleteFunc(d.keys, func(k string) bool { return k == name })
}

// Keys lists the members in dict order.
func (d *Data) Keys() []string { return d.keys }

// Object is request.data re-encoded as a JSON object in dict order, as
// json.dumps(request.data) writes it.
func (d *Data) Object() jsontext.Value {
	if !d.IsDict() {
		return d.root.raw
	}
	var b jsontext.Value = jsontext.Value("{")
	for i, k := range d.keys {
		if i > 0 {
			b = append(b, ',')
		}
		name, _ := json.Marshal(k)
		b = append(append(append(b, name...), ':'), d.fields[k].raw...)
	}
	return append(b, '}')
}

// objectKeys lists a JSON object's member names, each once, in order of
// first appearance (json.loads keeps that position for a duplicate).
func objectKeys(obj jsontext.Value) []string {
	dec := jsontext.NewDecoder(bytes.NewReader(obj), jsontext.AllowDuplicateNames(true))
	if _, err := dec.ReadToken(); err != nil { // {
		return nil
	}
	var keys []string
	seen := map[string]bool{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return keys
		}
		if k := tok.String(); !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
		if err := dec.SkipValue(); err != nil {
			return keys
		}
	}
	return keys
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

// Elems returns the elements of a JSON array.
func (v Value) Elems() ([]Value, bool) {
	if v.Kind() != '[' {
		return nil, false
	}
	var raw []jsontext.Value
	if err := json.Unmarshal(v.raw, &raw); err != nil {
		return nil, false
	}
	out := make([]Value, len(raw))
	for i, r := range raw {
		out[i] = Value{raw: r}
	}
	return out, true
}

// Member is dict.get(name) on a JSON object (the last duplicate wins, as in
// json.loads).
func (v Value) Member(name string) (Value, bool) {
	if v.Kind() != '{' {
		return Value{}, false
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(v.raw, &members, jsontext.AllowDuplicateNames(true)); err != nil {
		return Value{}, false
	}
	m, ok := members[name]
	return Value{raw: m}, ok
}

// JSONValue wraps an already-encoded JSON value.
func JSONValue(raw jsontext.Value) Value { return Value{raw: raw} }

// DataFromJSON is json.loads of a dict serialized by a view (requested
// data handed to a task), keeping member order.
func DataFromJSON(raw jsontext.Value) *Data {
	d := &Data{root: Value{raw: raw}, fields: map[string]Value{}}
	if raw.Kind() != '{' {
		d.typeName = pyTypeName(raw.Kind(), raw)
		return d
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(raw, &members, jsontext.AllowDuplicateNames(true)); err != nil {
		d.typeName = "str"
		return d
	}
	for k, m := range members {
		d.fields[k] = Value{raw: m}
	}
	d.keys = objectKeys(raw)
	return d
}
