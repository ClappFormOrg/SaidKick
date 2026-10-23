package models

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStringArrayValue(t *testing.T) {
	cases := []struct {
		name string
		in   StringArray
		want string
	}{
		{"nil stores empty array", nil, "{}"},
		{"empty", StringArray{}, "{}"},
		{"roles", StringArray{"developer", "auditor"}, "{developer,auditor}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.in.Value()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStringArrayRoundTrip(t *testing.T) {
	in := StringArray{"a b", `c"d`, "e,f", `g\h`, "NULL", ""}
	v, err := in.Value()
	require.NoError(t, err)

	var out StringArray
	require.NoError(t, out.Scan(v))
	assert.Equal(t, in, out)
}

func TestStringArrayScan(t *testing.T) {
	cases := []struct {
		name string
		src  any
		want StringArray
	}{
		{"null", nil, nil},
		{"empty string src", "{}", StringArray{}},
		{"string src", "{developer,deployer}", StringArray{"developer", "deployer"}},
		{"bytes src", []byte("{platform_admin}"), StringArray{"platform_admin"}},
		{"quoted", `{"a b","c\"d"}`, StringArray{"a b", `c"d`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a StringArray
			require.NoError(t, a.Scan(tc.src))
			assert.Equal(t, tc.want, a)
		})
	}

	t.Run("unsupported type", func(t *testing.T) {
		var a StringArray
		assert.Error(t, a.Scan(42))
	})
	t.Run("malformed literal", func(t *testing.T) {
		var a StringArray
		assert.Error(t, a.Scan("not-an-array"))
	})
}

// TestStringArrayEncodesAsQueryArgument checks that pgx, which receives the
// value unchanged from database/sql, can encode it for a text[] parameter in
// both wire formats.
func TestStringArrayEncodesAsQueryArgument(t *testing.T) {
	m := pgtype.NewMap()
	for _, format := range []int16{pgtype.TextFormatCode, pgtype.BinaryFormatCode} {
		buf, err := m.Encode(pgtype.TextArrayOID, format, StringArray{"developer"}, nil)
		require.NoError(t, err, "format %d", format)

		var out []string
		require.NoError(t, m.Scan(pgtype.TextArrayOID, format, buf, &out), "format %d", format)
		assert.Equal(t, []string{"developer"}, out)
	}
}
