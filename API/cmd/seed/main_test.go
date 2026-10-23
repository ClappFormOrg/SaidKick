package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckEnv(t *testing.T) {
	allowed := []string{"dev", "development", "local", "test", "ci", "DEV", "Test"}
	for _, env := range allowed {
		t.Run("allowed/"+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			assert.NoError(t, checkEnv())
		})
	}

	refused := []string{"", "production", "prod", "staging", "acceptance", "develop"}
	for _, env := range refused {
		t.Run("refused/"+env, func(t *testing.T) {
			t.Setenv("APP_ENV", env)
			err := checkEnv()
			require.Error(t, err)
			assert.Equal(t, "refusing to seed a production-like environment (APP_ENV="+env+")", err.Error())
		})
	}
}
