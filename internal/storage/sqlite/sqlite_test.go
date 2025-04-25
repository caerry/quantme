package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	// Add this blank import for the driver
	_ "github.com/mattn/go-sqlite3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"quantme/internal/event"
	"quantme/internal/storage"
)

// ... rest of the test code remains the same ...

func setupTestDB(t *testing.T) (storage.Storage, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_quantme.db")
	store := NewSQLiteStore(dbPath)
	ctx := context.Background()
    // This Init call internally uses sql.Open("sqlite3", ...)
    // The blank import above makes the "sqlite3" driver available here.
	err := store.Init(ctx)
	require.NoError(t, err, "Failed to initialize test database")

	cleanup := func() {
		err := store.Close()
		assert.NoError(t, err, "Failed to close test database")
		// os.RemoveAll(tempDir) // t.TempDir() handles cleanup
	}

	return store, cleanup
}
func TestSaveAndGetEvent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second) // Truncate for easier comparison

	testEvent := event.Event{
		Timestamp:   now,
		Type:        event.EventTypeCustom,
		AppName:     "TestApp",
		WindowTitle: "Test Window",
		Value:       123.45,
		Tag:         "TestTag",
		Notes:       "These are test notes.",
	}

	id, err := store.SaveEvent(ctx, testEvent)
	require.NoError(t, err)
	assert.Greater(t, id, int64(0))

	// Get the event back
	retrievedEvents, err := store.GetEvents(ctx, now.Add(-1*time.Minute), now.Add(1*time.Minute))
	require.NoError(t, err)
	require.Len(t, retrievedEvents, 1)

	retrieved := retrievedEvents[0]
	assert.Equal(t, id, retrieved.ID)
	assert.Equal(t, testEvent.Type, retrieved.Type)
	// Compare time after truncating retrieved time as well
	assert.Equal(t, testEvent.Timestamp, retrieved.Timestamp.Truncate(time.Second))
	assert.Equal(t, testEvent.AppName, retrieved.AppName)
	assert.Equal(t, testEvent.WindowTitle, retrieved.WindowTitle)
	assert.InDelta(t, testEvent.Value, retrieved.Value, 0.001)
	assert.Equal(t, testEvent.Tag, retrieved.Tag)
	assert.Equal(t, testEvent.Notes, retrieved.Notes)
}

func TestGetEventsFiltering(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	t1 := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	t2 := t1.Add(1 * time.Minute)
	t3 := t1.Add(5 * time.Minute)
	t4 := t1.Add(15 * time.Minute) // Outside initial range

	events := []event.Event{
		{Timestamp: t1, Type: event.EventTypeCustom, Tag: "A"},
		{Timestamp: t2, Type: event.EventTypeFocusChange, AppName: "App1"},
		{Timestamp: t3, Type: event.EventTypeCustom, Tag: "B"},
		{Timestamp: t4, Type: event.EventTypePomodoro, Tag: string(event.StateFocus)},
	}

	for _, e := range events {
		_, err := store.SaveEvent(ctx, e)
		require.NoError(t, err)
	}

	// Test time range
	retrieved, err := store.GetEvents(ctx, t1, t3) // Includes t1 and t3
	require.NoError(t, err)
	require.Len(t, retrieved, 3)
	assert.Equal(t, events[0].Tag, retrieved[0].Tag)
	assert.Equal(t, events[1].AppName, retrieved[1].AppName)
	assert.Equal(t, events[2].Tag, retrieved[2].Tag)

	// Test type filtering
	retrieved, err = store.GetEvents(ctx, t1.Add(-time.Hour), t4.Add(time.Hour), event.EventTypeCustom)
	require.NoError(t, err)
	require.Len(t, retrieved, 2)
	assert.Equal(t, events[0].Tag, retrieved[0].Tag)
	assert.Equal(t, events[2].Tag, retrieved[1].Tag)

	// Test multiple type filtering
	retrieved, err = store.GetEvents(ctx, t1.Add(-time.Hour), t4.Add(time.Hour), event.EventTypeFocusChange, event.EventTypePomodoro)
	require.NoError(t, err)
	require.Len(t, retrieved, 2)
	assert.Equal(t, events[1].AppName, retrieved[0].AppName)
	assert.Equal(t, events[3].Tag, retrieved[1].Tag)

	// Test no results
	retrieved, err = store.GetEvents(ctx, t1.Add(10*time.Hour), t4.Add(11*time.Hour))
	require.NoError(t, err)
	assert.Len(t, retrieved, 0)
}

func TestCloseDB(t *testing.T) {
	store, cleanup := setupTestDB(t)
	// Call cleanup explicitly to test Close
	cleanup()

	// Try saving after close (should fail)
	ctx := context.Background()
	_, err := store.SaveEvent(ctx, event.Event{Timestamp: time.Now(), Type: event.EventTypeCustom})
	assert.Error(t, err) // Expecting "sql: database is closed" or similar
}
