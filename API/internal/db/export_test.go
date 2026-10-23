package db

import "github.com/go-gormigrate/gormigrate/v2"

// MigrationByID exposes one versioned migration to the external tests.
func MigrationByID(id string) *gormigrate.Migration {
	for _, m := range migrations {
		if m.ID == id {
			return m
		}
	}
	return nil
}

// MigrationIDs lists every versioned migration ID in declaration order.
func MigrationIDs() []string {
	ids := make([]string, 0, len(migrations))
	for _, m := range migrations {
		ids = append(ids, m.ID)
	}
	return ids
}
