package models

import (
	"database/sql/driver"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgtype"
)

// typeMaps pools pgtype.Map values for StringArray. A Map caches encode and
// scan plans and is not safe for concurrent use, so each call borrows one.
var typeMaps = sync.Pool{New: func() any { return pgtype.NewMap() }}

// StringArray maps a Postgres text[] column to a Go string slice. It
// implements sql.Scanner and driver.Valuer so it works through database/sql
// and GORM without pulling in lib/pq.
type StringArray []string

// Scan decodes a text[] value in Postgres text format, which is the format
// the pgx database/sql driver returns for array columns.
func (a *StringArray) Scan(src any) error {
	var buf []byte
	switch v := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		buf = []byte(v)
	case []byte:
		buf = v
	default:
		return fmt.Errorf("StringArray: cannot scan %T", src)
	}

	m := typeMaps.Get().(*pgtype.Map)
	defer typeMaps.Put(m)

	var out []string
	if err := m.Scan(pgtype.TextArrayOID, pgtype.TextFormatCode, buf, &out); err != nil {
		return fmt.Errorf("StringArray: %w", err)
	}
	*a = out
	return nil
}

// Value encodes the slice as a text[] literal. A nil slice is stored as an
// empty array, never NULL.
func (a StringArray) Value() (driver.Value, error) {
	s := []string(a)
	if s == nil {
		s = []string{}
	}

	m := typeMaps.Get().(*pgtype.Map)
	defer typeMaps.Put(m)

	buf, err := m.Encode(pgtype.TextArrayOID, pgtype.TextFormatCode, s, nil)
	if err != nil {
		return nil, fmt.Errorf("StringArray: %w", err)
	}
	return string(buf), nil
}
