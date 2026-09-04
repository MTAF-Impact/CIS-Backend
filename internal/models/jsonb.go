package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB is a raw JSON document stored in a Postgres `jsonb` column.
//
// The coordinated-network detector's payloads (detection_run.parameters_json,
// network_account.score_contribution_json,
// cis_network_review_log.signal_profile_json) whose shape belongs to the
// detector rather than to this backend. Mapping them as typed structs here
// would make every detector change a backend change, so they are passed
// through as opaque documents and re-emitted verbatim.
type JSONB []byte

// Scan implements sql.Scanner.
func (j *JSONB) Scan(value any) error {
	if value == nil {
		*j = nil
		return nil
	}
	switch v := value.(type) {
	case []byte:
		buf := make([]byte, len(v))
		copy(buf, v)
		*j = buf
	case string:
		*j = JSONB(v)
	default:
		return fmt.Errorf("cannot scan %T into JSONB", value)
	}
	return nil
}

// Value implements driver.Valuer. A nil or empty document is stored as SQL
// NULL rather than as the string "null", so `IS NULL` behaves as expected.
//
// The document is handed to the driver as a string, never as []byte. Under
// PreferSimpleProtocol (the Supabase transaction pooler, see the StringList
// comment below) pgx interpolates arguments into the SQL text itself, and it
// renders a []byte as a bytea hex literal (a backslash-x hex string), which
// Postgres rejects for a jsonb column with "invalid input syntax for type
// json" (SQLSTATE 22P02). A string is quoted as a plain SQL string literal and
// casts into jsonb under both protocols.
func (j JSONB) Value() (driver.Value, error) {
	if len(j) == 0 {
		return nil, nil
	}
	return string(j), nil
}

// MarshalJSON emits the stored document inline instead of base64-encoding it,
// which is what the default []byte marshaller would do.
func (j JSONB) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// UnmarshalJSON stores the document verbatim.
func (j *JSONB) UnmarshalJSON(data []byte) error {
	if data == nil {
		*j = nil
		return nil
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	*j = buf
	return nil
}

// GormDataType tells AutoMigrate to create a jsonb column.
func (JSONB) GormDataType() string { return "jsonb" }

// MustJSONB marshals v into a JSONB document, returning nil on failure.
//
// Used where the value being stored is built by this backend and a marshalling
// error would mean a programming mistake rather than bad input.
func MustJSONB(v any) JSONB {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return buf
}

// StringList is a list of short strings stored as a JSON array in a `jsonb`
// column.
//
// Postgres `text[]` was the obvious alternative and is deliberately not used:
// the backend may run through Supabase's transaction-mode pooler with
// PreferSimpleProtocol enabled, where array values arrive as an unparsed
// `{a,b,c}` literal that would need hand-written quoting rules. jsonb round-trips
// identically under both protocols. The reference DDL in
// docs/sql/01_f5_reference_schema.sql declares these columns as jsonb for the
// same reason.
type StringList []string

// Scan implements sql.Scanner.
func (l *StringList) Scan(value any) error {
	if value == nil {
		*l = nil
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("cannot scan %T into StringList", value)
	}
	if len(raw) == 0 {
		*l = nil
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("scan StringList: %w", err)
	}
	*l = out
	return nil
}

// Value implements driver.Valuer. Returns a string, not the []byte json.Marshal
// hands back, for the reason spelled out on JSONB.Value.
func (l StringList) Value() (driver.Value, error) {
	if l == nil {
		return nil, nil
	}
	buf, err := json.Marshal([]string(l))
	if err != nil {
		return nil, err
	}
	return string(buf), nil
}

// GormDataType tells AutoMigrate to create a jsonb column.
func (StringList) GormDataType() string { return "jsonb" }

// Contains reports whether the list holds s.
func (l StringList) Contains(s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}
