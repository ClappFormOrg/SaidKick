package secrets

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPathFor_validFieldNames asserts that field names matching the
// ^[a-zA-Z][a-zA-Z0-9_]{0,62}$ security regex produce the canonical
// kv/data/deployments/<uuid>/<field> path without error.
func TestPathFor_validFieldNames(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV7())

	cases := []struct {
		name  string
		field string
	}{
		{"single letter", "a"},
		{"lowercase word", "apikey"},
		{"uppercase start", "Token"},
		{"mixed with digits", "field1"},
		{"with underscore", "db_password"},
		{"trailing underscore", "value_"},
		{"all caps with underscores", "DATABASE_URL"},
		{"boundary 63 chars", "a" + strings.Repeat("b", 62)}, // 1 + 62 = 63 total
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path, err := PathFor(id, tc.field)
			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("kv/data/deployments/%s/%s", id.String(), tc.field), path)
		})
	}
}

// TestPathFor_invalidFieldNames asserts that field names violating the
// path-validation regex are rejected with an error that errors.Is
// ErrInvalidFieldName — the control that blocks Vault path injection.
func TestPathFor_invalidFieldNames(t *testing.T) {
	t.Parallel()

	id := uuid.Must(uuid.NewV7())

	cases := []struct {
		name  string
		field string
	}{
		{"empty", ""},
		{"leading digit", "1field"},
		{"leading underscore", "_field"},
		{"contains slash", "a/b"},
		{"contains dot", "a.b"},
		{"contains dot dot", "a..b"},
		{"path traversal", "../secret"},
		{"contains space", "field name"},
		{"contains hyphen", "field-name"},
		{"boundary 64 chars", "a" + strings.Repeat("b", 63)}, // 1 + 63 = 64 total
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path, err := PathFor(id, tc.field)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidFieldName),
				"error must wrap ErrInvalidFieldName, got %v", err)
			assert.Empty(t, path, "no path must be returned for an invalid field name")
		})
	}
}

// TestInMemoryStore_PutGetRoundTrip asserts a value written with Put is
// returned verbatim by Get.
func TestInMemoryStore_PutGetRoundTrip(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := uuid.Must(uuid.NewV7())
	want := []byte("super-secret-value")

	path, err := store.Put(t.Context(), id, "apikey", want)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("kv/data/deployments/%s/apikey", id.String()), path)

	got, err := store.Get(t.Context(), id, "apikey")
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestInMemoryStore_GetMissing asserts a Get for a field that was never
// written returns ErrNotFound.
func TestInMemoryStore_GetMissing(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := uuid.Must(uuid.NewV7())

	got, err := store.Get(t.Context(), id, "missing")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotFound), "expected ErrNotFound, got %v", err)
	assert.Nil(t, got)
}

// TestInMemoryStore_InvalidFieldName asserts that Put and Get surface the
// ErrInvalidFieldName error (via PathFor) for invalid names.
func TestInMemoryStore_InvalidFieldName(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := uuid.Must(uuid.NewV7())

	path, err := store.Put(t.Context(), id, "../escape", []byte("x"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidFieldName), "Put must reject invalid field name, got %v", err)
	assert.Empty(t, path)

	got, err := store.Get(t.Context(), id, "../escape")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidFieldName), "Get must reject invalid field name, got %v", err)
	assert.Nil(t, got)
}

// TestInMemoryStore_DeleteAllScopedToDeployment asserts DeleteAll purges only
// the target deployment's secrets and leaves a second deployment untouched.
func TestInMemoryStore_DeleteAllScopedToDeployment(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	idA := uuid.Must(uuid.NewV7())
	idB := uuid.Must(uuid.NewV7())

	_, err := store.Put(t.Context(), idA, "apikey", []byte("a-key"))
	require.NoError(t, err)
	_, err = store.Put(t.Context(), idA, "password", []byte("a-pass"))
	require.NoError(t, err)
	_, err = store.Put(t.Context(), idB, "apikey", []byte("b-key"))
	require.NoError(t, err)

	require.NoError(t, store.DeleteAll(t.Context(), idA))

	// Deployment A's secrets are gone.
	_, err = store.Get(t.Context(), idA, "apikey")
	assert.True(t, errors.Is(err, ErrNotFound), "A/apikey must be deleted, got %v", err)
	_, err = store.Get(t.Context(), idA, "password")
	assert.True(t, errors.Is(err, ErrNotFound), "A/password must be deleted, got %v", err)

	// Deployment B's secret is intact.
	got, err := store.Get(t.Context(), idB, "apikey")
	require.NoError(t, err)
	assert.Equal(t, []byte("b-key"), got)
}

// TestInMemoryStore_PutCopiesInput asserts that mutating the caller's slice
// after Put does not change the stored value (defensive copy on write).
func TestInMemoryStore_PutCopiesInput(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := uuid.Must(uuid.NewV7())

	input := []byte("original")
	_, err := store.Put(t.Context(), id, "field", input)
	require.NoError(t, err)

	// Mutate the caller's backing array after the write.
	for i := range input {
		input[i] = 'X'
	}

	got, err := store.Get(t.Context(), id, "field")
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), got, "stored value must not alias the caller's slice")
}

// TestInMemoryStore_GetReturnsCopy asserts that mutating the slice returned by
// Get does not corrupt the value held in the store (defensive copy on read).
func TestInMemoryStore_GetReturnsCopy(t *testing.T) {
	t.Parallel()

	store := NewInMemoryStore()
	id := uuid.Must(uuid.NewV7())

	_, err := store.Put(t.Context(), id, "field", []byte("original"))
	require.NoError(t, err)

	got, err := store.Get(t.Context(), id, "field")
	require.NoError(t, err)

	// Mutate the returned slice.
	for i := range got {
		got[i] = 'X'
	}

	again, err := store.Get(t.Context(), id, "field")
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), again, "mutating a Get result must not change the store")
}

// TestNullStore_EveryMethodNotConfigured asserts NullStore returns
// ErrNotConfigured from Put, Get, and DeleteAll.
func TestNullStore_EveryMethodNotConfigured(t *testing.T) {
	t.Parallel()

	var store Store = NullStore{}
	id := uuid.Must(uuid.NewV7())

	path, err := store.Put(t.Context(), id, "field", []byte("x"))
	assert.True(t, errors.Is(err, ErrNotConfigured), "Put must return ErrNotConfigured, got %v", err)
	assert.Empty(t, path)

	val, err := store.Get(t.Context(), id, "field")
	assert.True(t, errors.Is(err, ErrNotConfigured), "Get must return ErrNotConfigured, got %v", err)
	assert.Nil(t, val)

	err = store.DeleteAll(t.Context(), id)
	assert.True(t, errors.Is(err, ErrNotConfigured), "DeleteAll must return ErrNotConfigured, got %v", err)
}

// TestNullStore_InvalidateClusterNoOp asserts InvalidateCluster is a no-op and
// does not panic.
func TestNullStore_InvalidateClusterNoOp(t *testing.T) {
	t.Parallel()

	var store Store = NullStore{}
	assert.NotPanics(t, func() {
		store.InvalidateCluster(uuid.Must(uuid.NewV7()))
	})
}
