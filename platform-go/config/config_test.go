package config_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/config"
)

func loader(env map[string]string, files map[string]string) *config.Loader {
	return config.NewLoaderFrom(config.Map(env), func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}
		return nil, errors.New("no such file")
	})
}

func TestScalarTypes(t *testing.T) {
	l := loader(map[string]string{"S": "x", "I": "42", "F": "1.5", "B": "true", "D": "250", "LIST": " a, b ,,c "}, nil)
	assert.Equal(t, "x", l.String("S", "def"))
	assert.Equal(t, "def", l.String("MISSING", "def"))
	assert.Equal(t, 42, l.Int("I", 1))
	assert.Equal(t, 1.5, l.Float("F", 0))
	assert.True(t, l.Bool("B", false))
	assert.Equal(t, 250*time.Millisecond, l.DurationMS("D", time.Second))
	assert.Equal(t, []string{"a", "b", "c"}, l.Strings("LIST", nil))
	assert.Equal(t, []string{"z"}, l.Strings("MISSING", []string{"z"}))
	assert.Equal(t, 7, l.Int("MISSING", 7))
	assert.Equal(t, 2.5, l.Float("MISSING", 2.5))
	assert.True(t, l.Bool("MISSING", true))
	assert.Equal(t, time.Second, l.DurationMS("MISSING", time.Second))
	assert.NoError(t, l.Err())
}

func TestFileVariantWinsAndIsTrimmed(t *testing.T) {
	l := loader(map[string]string{"DB_PASSWORD": "from-env", "DB_PASSWORD_FILE": "/run/secrets/pw"},
		map[string]string{"/run/secrets/pw": "from-file\n"})
	assert.Equal(t, "from-file", l.Required("DB_PASSWORD"))
	assert.NoError(t, l.Err())
}

func TestProblemsAreCollected(t *testing.T) {
	l := loader(map[string]string{"I": "abc", "F": "x", "B": "maybe", "D": "-5", "K_FILE": "/missing"}, nil)
	l.Required("NEEDED")
	assert.Equal(t, 9, l.Int("I", 9))
	assert.Equal(t, 1.0, l.Float("F", 1.0))
	assert.False(t, l.Bool("B", false))
	assert.Equal(t, time.Second, l.DurationMS("D", time.Second))
	assert.Equal(t, "", l.String("K", ""))
	err := l.Err()
	require.Error(t, err)
	for _, want := range []string{"NEEDED is required", "I:", "F:", "B:", "D:", "K_FILE:"} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestLoadBaseDefaultsAndOverrides(t *testing.T) {
	l := loader(nil, nil)
	b := config.LoadBase(l, "order-service", 8106, 50056)
	require.NoError(t, l.Err())
	assert.Equal(t, "order-service", b.ServiceName)
	assert.Equal(t, 8106, b.HTTPPort)
	assert.Equal(t, 50056, b.GRPCPort)
	assert.Equal(t, 2*time.Second, b.GRPCTimeout)
	assert.Equal(t, 0.5, b.BreakerError)
	assert.Equal(t, 10, b.BreakerMin)
	assert.Equal(t, 30*time.Second, b.BreakerReset)
	assert.False(t, b.IsProduction())

	l = loader(map[string]string{"APP_ENV": "production", "BREAKER_ERROR_PCT": "25", "KAFKA_BROKERS": "k1:9092,k2:9092"}, nil)
	b = config.LoadBase(l, "x", 1, 2)
	assert.True(t, b.IsProduction())
	assert.Equal(t, 0.25, b.BreakerError)
	assert.Equal(t, []string{"k1:9092", "k2:9092"}, b.KafkaBrokers)
}

func TestLoadBaseRejectsBadBreakerPercent(t *testing.T) {
	l := loader(map[string]string{"BREAKER_ERROR_PCT": "0"}, nil)
	b := config.LoadBase(l, "x", 1, 2)
	assert.Equal(t, 0.5, b.BreakerError)
	assert.ErrorContains(t, l.Err(), "BREAKER_ERROR_PCT")
}

func TestNewLoaderUsesProcessEnvironment(t *testing.T) {
	t.Setenv("ECOM_TEST_VALUE", "from-process")
	assert.Equal(t, "from-process", config.NewLoader().String("ECOM_TEST_VALUE", ""))
}
