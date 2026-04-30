package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorePathUsesNomadlDB(t *testing.T) {
	r := require.New(t)

	r.Equal(filepath.Join("/tmp/config", "nomadl", "nomadl.db"), storePath("/tmp/config"))
}

func TestAppStoreSearchHistory(t *testing.T) {
	r := require.New(t)

	path := filepath.Join(t.TempDir(), "nomadl.db")
	store, err := openAppStore(path)
	r.NoError(err)
	defer store.Close()

	_, err = os.Stat(path)
	r.NoError(err)

	apiContext := searchHistoryContext{service: "api", namespace: "default", region: "global"}
	webContext := searchHistoryContext{service: "web", namespace: "default", region: "global"}

	r.NoError(store.SaveSearch("@msg:/fail/", apiContext))
	r.NoError(store.SaveSearch("@level:info", webContext))
	r.NoError(store.SaveSearch(" @msg:/fail/ ", apiContext))
	r.NoError(store.SaveSearch("   ", apiContext))

	apiSearches, err := store.RecentSearches(apiContext, 10)
	r.NoError(err)
	r.Equal([]string{"@msg:/fail/", "@level:info"}, apiSearches)

	webSearches, err := store.RecentSearches(webContext, 10)
	r.NoError(err)
	r.Equal([]string{"@level:info", "@msg:/fail/"}, webSearches)
}

func TestAppStorePreferences(t *testing.T) {
	r := require.New(t)

	path := filepath.Join(t.TempDir(), "nomadl.db")
	store, err := openAppStore(path)
	r.NoError(err)
	defer store.Close()

	defaults := defaultAppPreferences()
	preferences, err := store.LoadPreferences(defaults)
	r.NoError(err)
	r.Equal(defaults, preferences)

	want := appPreferences{
		logType:       "stdout",
		wrapLogs:      true,
		follow:        false,
		highlightJSON: false,
	}
	r.NoError(store.SavePreferences(want))

	got, err := store.LoadPreferences(defaults)
	r.NoError(err)
	r.Equal(want, got)
}
