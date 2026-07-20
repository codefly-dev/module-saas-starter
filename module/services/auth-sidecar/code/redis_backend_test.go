package main

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRedisClientOptionsUseBoundedPoolAndTimeouts(t *testing.T) {
	options, err := redisClientOptions("redis://:secret@cache.internal:6379/3")
	require.NoError(t, err)
	require.Equal(t, "cache.internal:6379", options.Addr)
	require.Equal(t, "secret", options.Password)
	require.Equal(t, 3, options.DB)
	require.Equal(t, 10, options.PoolSize)
	require.Equal(t, 1, options.MinIdleConns)
	require.Equal(t, 5, options.MaxIdleConns)
	require.Equal(t, 5*time.Second, options.DialTimeout)
	require.Equal(t, 2*time.Second, options.ReadTimeout)
	require.Equal(t, 2*time.Second, options.WriteTimeout)
	require.Equal(t, 3*time.Second, options.PoolTimeout)
}

func TestRedisClientOptionsRequireTLS12ForRediss(t *testing.T) {
	options, err := redisClientOptions("rediss://cache.internal:6380")
	require.NoError(t, err)
	require.NotNil(t, options.TLSConfig)
	require.Equal(t, uint16(tls.VersionTLS12), options.TLSConfig.MinVersion)
}

func TestRedisClientOptionsAcceptCodeflyHostPortAndRejectInvalidURL(t *testing.T) {
	options, err := redisClientOptions("localhost:6379")
	require.NoError(t, err)
	require.Equal(t, "localhost:6379", options.Addr)

	_, err = redisClientOptions("://bad")
	require.Error(t, err)
}
