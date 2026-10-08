package drf

import (
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"plane-lite/server/internal/httpx"
)

// Validator runs a partial (PATCH) serializer over request.data: absent
// fields are skipped, present ones are validated and coerced, and errors
// collect per field like serializer.errors.
type Validator struct {
	data *Data
	loc  *time.Location
	errs map[string][]string
	// indexed holds ListField child errors: field -> index -> messages.
	indexed map[string]map[int][]string
}

// NewValidator starts validation; loc is the active (user) timezone that
// naive datetimes are read in.
func NewValidator(d *Data, loc *time.Location) *Validator {
	v := &Validator{data: d, loc: loc, errs: map[string][]string{}, indexed: map[string]map[int][]string{}}
	if !d.IsDict() {
		v.errs["non_field_errors"] = []string{"Invalid data. Expected a dictionary, but got " + d.typeName + "."}
	}
	return v
}

// Add records a validation error, as a validate_<field> method raising
// ValidationError would.
func (v *Validator) Add(field, msg string) { v.errs[field] = append(v.errs[field], msg) }

func (v *Validator) Valid() bool { return len(v.errs) == 0 && len(v.indexed) == 0 }

// Err is the 400 response for serializer.errors, or nil.
func (v *Validator) Err() error {
	if v.Valid() {
		return nil
	}
	if len(v.indexed) == 0 {
		return httpx.Body(http.StatusBadRequest, v.errs)
	}
	body := map[string]any{}
	for k, msgs := range v.errs {
		body[k] = msgs
	}
	for k, byIdx := range v.indexed {
		m := map[string][]string{}
		for i, msgs := range byIdx {
			m[strconv.Itoa(i)] = msgs
		}
		body[k] = m
	}
	return httpx.Body(http.StatusBadRequest, body)
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
	msgSlug     = `Enter a valid "slug" consisting of letters, numbers, underscores or hyphens.`
	msgRequired = "This field is required."
	msgInt      = "A valid integer is required."
	msgDateTime = "Datetime has wrong format. Use one of these formats instead: YYYY-MM-DDThh:mm[:ss[.uuuuuu]][+HH:MM|-HH:MM|Z]."
	msgDate     = "Date has wrong format. Use one of these formats instead: YYYY-MM-DD."
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
	Slug       bool // SlugField: adds its RegexValidator
	// Before are validators passed to the field (model field validators,
	// UniqueValidator), which DRF runs ahead of the CharField's own; each
	// returns an error message or "".
	Before []func(string) string
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
	default: // bools, lists and dicts: fail("invalid"), whose message
		// SlugField and URLField override
		switch {
		case f.Slug:
			v.Add(name, msgSlug)
		case f.URL:
			v.Add(name, msgURL)
		default:
			v.Add(name, msgNotStr)
		}
		return nil, false
	}
	s := PyStr(val)
	if !f.NoTrim {
		s = PyStrip(s)
	}
	var errs []string
	for _, check := range f.Before {
		if msg := check(s); msg != "" {
			errs = append(errs, msg)
		}
	}
	if f.MaxLength > 0 && utf8.RuneCountInString(s) > f.MaxLength {
		errs = append(errs, fmt.Sprintf("Ensure this field has no more than %d characters.", f.MaxLength))
	}
	if strings.ContainsRune(s, 0) {
		errs = append(errs, msgNullChar)
	}
	if f.URL && !ValidURL(s) {
		errs = append(errs, msgURL)
	}
	if f.Slug && !slugRe.MatchString(s) {
		errs = append(errs, msgSlug)
	}
	if len(errs) > 0 {
		v.errs[name] = append(v.errs[name], errs...)
		return nil, false
	}
	return &s, true
}

var slugRe = regexp.MustCompile(`^[-a-zA-Z0-9_]+$`)

// Require reports "This field is required." for each absent field, as a
// full (non-partial) serializer does.
func (v *Validator) Require(names ...string) {
	if !v.data.IsDict() {
		return
	}
	for _, n := range names {
		if !v.data.Has(n) {
			v.Add(n, msgRequired)
		}
	}
}

// Int validates an IntegerField with the model's range validators.
func (v *Validator) Int(name string, min, max int64) (int64, bool) {
	val, ok := v.get(name, false, false)
	if !ok {
		return 0, false
	}
	if val.IsNull() {
		v.Add(name, msgNull)
		return 0, false
	}
	return v.intValue(name, val, min, max)
}

// IntNull validates a nullable IntegerField. A nil result means null.
func (v *Validator) IntNull(name string, min, max int64) (*int64, bool) {
	val, ok := v.get(name, true, false)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		return nil, true
	}
	n, ok := v.intValue(name, val, min, max)
	if !ok {
		return nil, false
	}
	return &n, true
}

func (v *Validator) intValue(name string, val Value, min, max int64) (int64, bool) {
	// int(re.sub(r"\.0*\s*$", "", str(data)))
	s := PyStr(val)
	if val.IsString() && utf8.RuneCountInString(s) > 1000 {
		v.Add(name, "String value too large.")
		return 0, false
	}
	n, ok := parsePyIntString(intDecimalRe.ReplaceAllString(s, ""))
	if !ok || val.Kind() == 't' || val.Kind() == 'f' || val.Kind() == '[' || val.Kind() == '{' {
		v.Add(name, msgInt)
		return 0, false
	}
	switch {
	case n.Cmp(big.NewInt(min)) < 0:
		v.Add(name, fmt.Sprintf("Ensure this value is greater than or equal to %d.", min))
		return 0, false
	case n.Cmp(big.NewInt(max)) > 0:
		v.Add(name, fmt.Sprintf("Ensure this value is less than or equal to %d.", max))
		return 0, false
	}
	return n.Int64(), true
}

var intDecimalRe = regexp.MustCompile(`\.0*\s*$`)

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

// Date validates a DateField. A nil result means null.
func (v *Validator) Date(name string, allowNull bool) (*time.Time, bool) {
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
		if t, ok := ParseDate(val.Str()); ok {
			return &t, true
		}
	}
	v.Add(name, msgDate)
	return nil, false
}

// PKList validates a ListField(child=PrimaryKeyRelatedField(...)) of UUID
// keys; exists checks the child's queryset.
func (v *Validator) PKList(name string, exists func(uuid.UUID) (bool, error)) ([]uuid.UUID, bool, error) {
	val, ok := v.get(name, false, false)
	if !ok {
		return nil, false, nil
	}
	if val.IsNull() {
		v.Add(name, msgNull)
		return nil, false, nil
	}
	elems, isList := val.Elems()
	if !isList {
		v.Add(name, `Expected a list of items but got type "`+pyTypeName(val.Kind(), val.raw)+`".`)
		return nil, false, nil
	}
	out := make([]uuid.UUID, 0, len(elems))
	errs := map[int][]string{}
	for i, el := range elems {
		msg, id, err := pkChild(el, exists)
		if err != nil {
			return nil, false, err
		}
		if msg != "" {
			errs[i] = []string{msg}
			continue
		}
		out = append(out, id)
	}
	if len(errs) > 0 {
		v.indexed[name] = errs
		return nil, false, nil
	}
	return out, true, nil
}

// CharList validates a non-nullable ListField(child=CharField(...)), the
// field ModelSerializer builds for an ArrayField of CharField or URLField.
// maxItems > 0 adds the ArrayField's ArrayMaxLengthValidator(size), which
// runs on the whole list once every child is valid.
func (v *Validator) CharList(name string, child CharField, maxItems int) ([]string, bool) {
	val, ok := v.get(name, false, false)
	if !ok {
		return nil, false
	}
	if val.IsNull() {
		v.Add(name, msgNull)
		return nil, false
	}
	elems, isList := val.Elems()
	if !isList {
		v.Add(name, `Expected a list of items but got type "`+pyTypeName(val.Kind(), val.raw)+`".`)
		return nil, false
	}
	out := make([]string, 0, len(elems))
	errs := map[int][]string{}
	for i, el := range elems {
		sub := &Validator{data: &Data{root: Value{raw: jsontext.Value("{}")}, fields: map[string]Value{"": el}, keys: []string{""}},
			loc: v.loc, errs: map[string][]string{}, indexed: map[string]map[int][]string{}}
		s, ok := sub.Char("", child)
		switch {
		case !ok:
			errs[i] = sub.errs[""]
		case s != nil: // a non-null child never yields None
			out = append(out, *s)
		}
	}
	if len(errs) > 0 {
		v.indexed[name] = errs
		return nil, false
	}
	if maxItems > 0 && len(out) > maxItems {
		v.Add(name, fmt.Sprintf("List contains %d items, it should contain no more than %d.", len(out), maxItems))
		return nil, false
	}
	return out, true
}

// pkChild runs a non-null PrimaryKeyRelatedField on one value, returning its
// error message, if any.
func pkChild(val Value, exists func(uuid.UUID) (bool, error)) (string, uuid.UUID, error) {
	switch {
	case val.IsNull():
		return msgNull, uuid.Nil, nil
	case val.Kind() == 't' || val.Kind() == 'f':
		return "Incorrect type. Expected pk value, received bool.", uuid.Nil, nil
	}
	id, ok := uuidInput(val, false)
	if !ok {
		return "“" + PyStr(val) + "” is not a valid UUID.", uuid.Nil, nil
	}
	found, err := exists(id)
	if err != nil {
		return "", uuid.Nil, err
	}
	if !found {
		return `Invalid pk "` + PyStr(val) + `" - object does not exist.`, uuid.Nil, nil
	}
	return "", id, nil
}

// PK validates a PrimaryKeyRelatedField to a UUID-keyed model; exists
// checks the related queryset. A nil result means null.
func (v *Validator) PK(name string, allowNull bool, exists func(uuid.UUID) (bool, error)) (*uuid.UUID, bool, error) {
	val, ok := v.get(name, allowNull, false)
	if !ok {
		return nil, false, nil
	}
	// RelatedField.run_validation forces empty strings to None first (the
	// web sends state_id "" when no state is picked).
	if val.IsString() && val.Str() == "" {
		val = Value{raw: jsontext.Value("null")}
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

// Float validates a non-nullable FloatField: float(data), with strings over
// 1000 characters refused first.
func (v *Validator) Float(name string) (float64, bool) {
	val, ok := v.get(name, false, false)
	if !ok {
		return 0, false
	}
	if val.IsNull() {
		v.Add(name, msgNull)
		return 0, false
	}
	if val.IsString() && utf8.RuneCountInString(val.Str()) > 1000 {
		v.Add(name, "String value too large.")
		return 0, false
	}
	f, ok := PyFloat(val)
	if !ok {
		v.Add(name, "A valid number is required.")
		return 0, false
	}
	return f, true
}

// PyFloat is Python's float() on a request value.
func PyFloat(val Value) (float64, bool) {
	switch val.Kind() {
	case 't':
		return 1, true
	case 'f':
		return 0, true
	case '0':
		f, err := strconv.ParseFloat(string(val.raw), 64)
		return f, err == nil || isRangeErr(err)
	case '"':
		return pyFloatString(val.Str())
	}
	return 0, false
}

// pyFloatString parses a float literal the way float(str) does: surrounding
// whitespace, underscores between digits, inf/nan spellings; no hex.
func pyFloatString(s string) (float64, bool) {
	s = PyStrip(s)
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return 0, false
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	case "nan":
		return math.NaN(), true // ParseFloat refuses a signed NaN
	}
	// Unicode decimal digits count as their ASCII forms.
	var ascii []byte
	for _, r := range s {
		if r > unicode.MaxASCII && unicode.IsDigit(r) {
			r = rune('0' + digitValue(r))
		}
		if r > unicode.MaxASCII {
			return 0, false
		}
		ascii = append(ascii, byte(r))
	}
	var b strings.Builder
	prev := byte(0)
	for i, c := range ascii {
		switch {
		case c == '_':
			if !isDigit(prev) || i+1 >= len(ascii) || !isDigit(ascii[i+1]) {
				return 0, false
			}
		case isDigit(c) || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-':
			b.WriteByte(c)
		default:
			return 0, false
		}
		prev = c
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	return f, err == nil || isRangeErr(err)
}

// isRangeErr: Python overflows to inf (or underflows to 0) instead of failing.
func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// PyUpper is str.upper(), with its full case mappings ("ß" becomes "SS").
func PyUpper(s string) string { return cases.Upper(language.Und).String(s) }

// PyIntString is int(s) for a str: whitespace, a sign, underscores between
// digits and any Unicode decimal digits.
func PyIntString(s string) (*big.Int, bool) { return parsePyIntString(s) }
