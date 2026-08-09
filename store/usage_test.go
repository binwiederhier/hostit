package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAssistantUsageAccumulatesAndSums(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, OwnerID: "u_1"}))
	require.NoError(t, s.AddApp(&App{Name: "shop", Port: 10001, OwnerID: "u_1"}))
	require.NoError(t, s.AddApp(&App{Name: "wiki", Port: 10002, OwnerID: "u_2"}))

	require.NoError(t, s.AddAssistantUsage("blog", AssistantUsage{InputTokens: 100, OutputTokens: 50, CacheWriteTokens: 20, CacheReadTokens: 1000}))
	require.NoError(t, s.AddAssistantUsage("blog", AssistantUsage{InputTokens: 100, OutputTokens: 50})) // accumulates
	require.NoError(t, s.AddAssistantUsage("shop", AssistantUsage{InputTokens: 10}))
	require.NoError(t, s.AddAssistantUsage("wiki", AssistantUsage{OutputTokens: 7}))
	// Recording for a non-existent app is a no-op, not an error.
	require.NoError(t, s.AddAssistantUsage("ghost", AssistantUsage{InputTokens: 999}))

	byOwner, err := s.UsageByOwner()
	require.NoError(t, err)
	// u_1 = blog (200 in, 100 out, 20 write, 1000 read) + shop (10 in).
	assert.Equal(t, int64(210), byOwner["u_1"].InputTokens)
	assert.Equal(t, int64(100), byOwner["u_1"].OutputTokens)
	assert.Equal(t, int64(20), byOwner["u_1"].CacheWriteTokens)
	assert.Equal(t, int64(1000), byOwner["u_1"].CacheReadTokens)
	assert.Equal(t, int64(7), byOwner["u_2"].OutputTokens)
}

func TestAssistantUsageFollowsRename(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	require.NoError(t, s.AddApp(&App{Name: "blog", Port: 10000, OwnerID: "u_1"}))
	require.NoError(t, s.AddAssistantUsage("blog", AssistantUsage{InputTokens: 100}))

	// Usage keys on app_id, so after a rename it accumulates onto the same row.
	_, err := s.db.Exec(`UPDATE app SET name = 'shop' WHERE name = 'blog'`)
	require.NoError(t, err)
	require.NoError(t, s.AddAssistantUsage("shop", AssistantUsage{InputTokens: 5}))

	byOwner, err := s.UsageByOwner()
	require.NoError(t, err)
	assert.Equal(t, int64(105), byOwner["u_1"].InputTokens, "usage survived the rename and kept accumulating")
}
