package drf

import (
	"fmt"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"

	"plane-lite/server/internal/httpx"
)

// Validator runs a partial (PATCH) serializer over request.data: absent
// fields are skipped, present ones are validated and coerced, and errors
// collect per field like serializer.errors.
type Validator struct {
	data *Data
	loc  *time.Location
	errs map[string][]string
}

// NewValidator starts validation; loc is the active (user) timezone that
// naive datetimes are read in.
func NewValidator(d *Data, loc *time.Location) *Validator {
	v := &Validator{data: d, loc: loc, errs: map[string][]string{}}
	if !d.IsDict() {
		v.errs["non_field_errors"] = []string{"Invalid data. Expected a dictionary, but got " + d.typeName + "."}
	}
	return v
}

// Add records a validation error, as a validate_<field> method raising
// ValidationError would.
func (v *Validator) Add(field, msg string) { v.errs[field] = append(v.errs[field], msg) }

func (v *Validator) Valid() bool { return len(v.errs) == 0 }

// Err is the 400 response for serializer.errors, or nil.
func (v *Validator) Err() error {
	if v.Valid() {
		return nil
	}
	return httpx.Body(http.StatusBadRequest, v.errs)
}

const (
	msgNull     = "This field may not be null."
	msgBlank    = "This field may not be blank."
	msgNotStr   = "Not a valid string."
	msgBool     = "Must be a valid boolean."
	msgUUID     = "Must be a valid UUID."
	msgJSON     = "Value must be valid JSON."
	msgNullChar = "Null characters are not allowed."
	msgURL      = "Enter a valid URL."
	msgDateTime = "Datetime has wrong format. Use one of these formats instead: YYYY-MM-DDThh:mm[:ss[.uuuuuu]][+HH:MM|-HH:MM|Z]."
)

// get is Field.get_value: the value for name, or ok=false when absent from
// a non-dict body, or skipped (partial update). Empty form values become
// null for nullable fields, as DRF's html-input handling does.
func (v *Validator) get(name string, allowNull, allowBlank bool) (Value, bool) {
	if !v.data.IsDict() {
		return Value{}, false
	}
	val, ok := v.data.Get(name)
	if !ok {
		return val, false
	}
	if val.form && allowNull && val.Str() == "" && !allowBlank {
		return Value{raw: jsontext.Value("null")}, true
	}
	return val, true
}

// CharField options. TrimWhitespace defaults on, as in DRF.
type CharField struct {
	MaxLength  int
	AllowBlank bool
	AllowNull  bool
	NoTrim     bool
	URL        bool // URLField: adds Django's URLValidator
}

// Char validates a CharField (or URLField). A nil result means null.
func (v *Validator) Char(name string, f CharField) (*string, bool) {
	val, ok := v.get(name, f.AllowNull, f.AllowBlank)
	if !ok {
		return nil, false
	}
	// CharField.run_validation: blank checks come before null handling.
	if val.IsString() {
		s := val.Str()
		if s == "" || (!f.NoTrim && PyStrip(s) == "") {
			if !f.AllowBlank {
				v.Add(name, msgBlank)
				return nil, false
			}
			empty := ""
			return &empty, true
		}
	}
	if val.IsNull() {
		if !f.AllowNull {
			v.Add(name, msgNull)
			return nil, false
		}
		return nil, true
	}
	switch val.Kind() {
	case '"', '0':
	default: // bools, lists and dicts
		v.Add(name, msgNotStr)
		return nil, false
	}
	s := PyStr(val)
	if !f.NoTrim {
		s = PyStrip(s)
	}
	var errs []string
	if f.MaxLength > 0 && utf8.RuneCountInString(s) > f.MaxLength {
		errs = append(errs, fmt.Sprintf("Ensure this field has no more than %d characters.", f.MaxLength))
	}
	if strings.ContainsRune(s, 0) {
		errs = append(errs, msgNullChar)
	}
	if f.URL && !ValidURL(s) {
		errs = append(errs, msgURL)
	}
	if len(errs) > 0 {
		v.errs[name] = append(v.errs[name], errs...)
		return nil, false
	}
	return &s, true
}

// Bool validates a non-nullable BooleanField.
func (v *Validator) Bool(name string) (bool, bool) {
	val, ok := v.get(name, false, false)
	if !ok {
		return false, false
	}
	if val.IsNull() {
		v.Add(name, msgNull)
		return false, false
	}
	if b, ok := PyBool(val); ok {
		return b, true
	}
	v.Add(name, msgBool)
	return false, false
}

// PyBool is BooleanField.to_internal_value's TRUE_VALUES/FALSE_VALUES
// lookup (numbers compare by value, so 1.0 is True).
func PyBool(val Value) (bool, bool) {
	switch val.Kind() {
	case 't':
		return true, true
	case 'f':
		return false, true
	case '"':
		switch val.Str() {
		case "t", "T", "y", "Y", "yes", "Yes", "YES", "true", "True", "TRUE", "on", "On", "ON", "1":
			return true, true
		case "f", "F", "n", "N", "no", "No", "NO", "false", "False", "FALSE", "off", "Off", "OFF", "0":
			return false, true
		}
	case '0':
		if f, err := strconv.ParseFloat(string(val.raw), 64); err == nil {
			switch f {
			case 1:
				return true, true
			case 0:
				return false, true
			}
		}
	}
	return false, false
}

// ChoiceField options.
type ChoiceField struct {
	AllowBlank bool
	AllowNull  bool
}

// Choice validates a ChoiceField: str(input) must equal a choice key.
func (v *Validator) Choice(name string, choices []string, f ChoiceField) (*string, bool) {
	val, ok := v.get(name, f.AllowNull, f.AllowBlank)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		if !f.AllowNull {
			v.Add(name, msgNull)
			return nil, false
		}
		return nil, true
	}
	s := PyStr(val)
	if val.IsString() && s == "" && f.AllowBlank {
		return &s, true
	}
	if !slices.Contains(choices, s) {
		v.Add(name, `"`+s+`" is not a valid choice.`)
		return nil, false
	}
	return &s, true
}

// JSON validates a JSONField; any JSON value is accepted. Null comes back
// as the literal null.
func (v *Validator) JSON(name string, allowNull bool) (jsontext.Value, bool) {
	val, ok := v.get(name, allowNull, false)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		if !allowNull {
			v.Add(name, msgNull)
			return nil, false
		}
		return val.raw, true
	}
	if val.form {
		raw := jsontext.Value(val.Str())
		if raw.Compact() != nil {
			v.Add(name, msgJSON)
			return nil, false
		}
		return raw, true
	}
	return val.raw, true
}

// UUID validates a UUIDField. A nil result means null.
func (v *Validator) UUID(name string, allowNull bool) (*uuid.UUID, bool) {
	val, ok := v.get(name, allowNull, false)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		if !allowNull {
			v.Add(name, msgNull)
			return nil, false
		}
		return nil, true
	}
	if id, ok := uuidInput(val, true); ok {
		return &id, true
	}
	v.Add(name, msgUUID)
	return nil, false
}

// uuidInput is uuid.UUID(int=...) for ints (bools included, being ints in
// Python) and uuid.UUID(hex=...) for strings.
func uuidInput(val Value, boolsAreInts bool) (uuid.UUID, bool) {
	switch val.Kind() {
	case '"':
		return ParseUUID(val.Str())
	case 't', 'f':
		if boolsAreInts {
			return uuidFromInt(big.NewInt(map[bool]int64{true: 1, false: 0}[val.Kind() == 't']))
		}
	case '0':
		if isIntLiteral(string(val.raw)) {
			if n, ok := new(big.Int).SetString(string(val.raw), 10); ok {
				return uuidFromInt(n)
			}
		}
	}
	return uuid.Nil, false
}

// DateTime validates a DateTimeField. A nil result means null.
func (v *Validator) DateTime(name string, allowNull bool) (*time.Time, bool) {
	val, ok := v.get(name, allowNull, false)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		if !allowNull {
			v.Add(name, msgNull)
			return nil, false
		}
		return nil, true
	}
	if val.IsString() {
		if t, ok := ParseDateTime(val.Str(), v.loc); ok {
			return &t, true
		}
	}
	v.Add(name, msgDateTime)
	return nil, false
}

// PK validates a PrimaryKeyRelatedField to a UUID-keyed model; exists
// checks the related queryset. A nil result means null.
func (v *Validator) PK(name string, allowNull bool, exists func(uuid.UUID) (bool, error)) (*uuid.UUID, bool, error) {
	val, ok := v.get(name, allowNull, false)
	if !ok {
		return nil, false, nil
	}
	if val.IsNull() {
		if !allowNull {
			v.Add(name, msgNull)
			return nil, false, nil
		}
		return nil, true, nil
	}
	if val.Kind() == 't' || val.Kind() == 'f' {
		v.Add(name, "Incorrect type. Expected pk value, received bool.")
		return nil, false, nil
	}
	// queryset.get(pk=data) runs Django's UUIDField.to_python, whose
	// ValidationError the serializer reports under the field.
	id, ok := uuidInput(val, false)
	if !ok {
		v.Add(name, "“"+PyStr(val)+"” is not a valid UUID.")
		return nil, false, nil
	}
	found, err := exists(id)
	if err != nil {
		return nil, false, err
	}
	if !found {
		v.Add(name, `Invalid pk "`+PyStr(val)+`" - object does not exist.`)
		return nil, false, nil
	}
	return &id, true, nil
}

// Decode unmarshals a validated JSON value.
func Decode(raw jsontext.Value, out any) error { return json.Unmarshal(raw, out) }
