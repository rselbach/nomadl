package main

import "time"

type appConfig struct {
	addr            string
	token           string
	namespace       string
	region          string
	logType         string
	storePath       string
	preferences     appPreferences
	preferencesSet  bool
	tailBytes       int64
	maxLines        int
	refreshInterval time.Duration
}

type appPreferences struct {
	logType       string
	wrapLogs      bool
	follow        bool
	highlightJSON bool
}

func defaultAppPreferences() appPreferences {
	return appPreferences{
		logType:       "stderr",
		follow:        true,
		highlightJSON: true,
	}
}

func isValidLogType(logType string) bool {
	switch logType {
	case "stdout", "stderr", "both":
		return true
	default:
		return false
	}
}
