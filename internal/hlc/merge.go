package hlc

import (
	"encoding/json"
	"errors"
)

// FieldState bundles a field-level LWW row: per-field HLC strings
// keyed by field name + the matching field values. A change-log
// entry's "payload" field stores this shape (back-compat: when
// FieldHLCs is empty, the entry is row-level LWW — every field
// shares the row's own HLC).
type FieldState struct {
	// FieldHLCs is the per-field timestamp. Map key is the field
	// name as it appears on the wire (e.g. "color", "note").
	FieldHLCs map[string]string `json:"field_hlcs,omitempty"`
	// Fields is the per-field value. Same keys as FieldHLCs.
	Fields map[string]any `json:"fields,omitempty"`
}

// Merge takes two FieldStates that describe the same row (one
// local, one remote) and returns the merged state. For each field,
// whichever side has the higher HLC wins. Fields present on only
// one side pass through.
//
// Tombstones: if either side carries a special "deleted" sentinel
// (handled by the caller, not here), the caller picks the correct
// side via Less() on the row's overall hlc. This function only
// merges live field maps.
func Merge(local, remote FieldState) (FieldState, error) {
	out := FieldState{
		FieldHLCs: make(map[string]string, len(local.FieldHLCs)+len(remote.FieldHLCs)),
		Fields:    make(map[string]any, len(local.Fields)+len(remote.Fields)),
	}
	// Walk every field in either side.
	allFields := make(map[string]struct{}, len(local.Fields)+len(remote.Fields))
	for f := range local.Fields {
		allFields[f] = struct{}{}
	}
	for f := range remote.Fields {
		allFields[f] = struct{}{}
	}
	for f := range allFields {
		localTS, localOK := local.FieldHLCs[f]
		remoteTS, remoteOK := remote.FieldHLCs[f]
		switch {
		case localOK && !remoteOK:
			out.FieldHLCs[f] = localTS
			out.Fields[f] = local.Fields[f]
		case !localOK && remoteOK:
			out.FieldHLCs[f] = remoteTS
			out.Fields[f] = remote.Fields[f]
		case localOK && remoteOK:
			lhs, err := Parse(localTS)
			if err != nil {
				return FieldState{}, err
			}
			rhs, err := Parse(remoteTS)
			if err != nil {
				return FieldState{}, err
			}
			if lhs.Less(rhs) {
				out.FieldHLCs[f] = remoteTS
				out.Fields[f] = remote.Fields[f]
			} else {
				out.FieldHLCs[f] = localTS
				out.Fields[f] = local.Fields[f]
			}
		default:
			// Both sides have the field value but no timestamp —
			// pathological. Treat as not-present.
		}
	}
	return out, nil
}

// FromJSON decodes a JSON payload into a FieldState. Tolerant of
// the legacy shape (no field_hlcs key) — returns a state with an
// empty FieldHLCs map, which Merge treats as "fields without
// timestamps."
func FromJSON(raw json.RawMessage) (FieldState, error) {
	if len(raw) == 0 {
		return FieldState{}, nil
	}
	var out FieldState
	if err := json.Unmarshal(raw, &out); err != nil {
		return FieldState{}, err
	}
	return out, nil
}

// ToJSON encodes a FieldState back to the wire format.
func ToJSON(s FieldState) (json.RawMessage, error) {
	if s.Fields == nil && s.FieldHLCs == nil {
		return nil, errors.New("empty FieldState")
	}
	return json.Marshal(s)
}
