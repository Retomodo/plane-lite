package drf

import "github.com/google/uuid"

// UUIDList validates a non-nullable ListField(child=UUIDField()); child
// errors are reported by index, as ListField does.
func (v *Validator) UUIDList(name string) ([]uuid.UUID, bool) {
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
	out := make([]uuid.UUID, 0, len(elems))
	errs := map[int][]string{}
	for i, el := range elems {
		if el.IsNull() {
			errs[i] = []string{msgNull}
			continue
		}
		id, ok := uuidInput(el, true)
		if !ok {
			errs[i] = []string{msgUUID}
			continue
		}
		out = append(out, id)
	}
	if len(errs) > 0 {
		v.indexed[name] = errs
		return nil, false
	}
	return out, true
}
