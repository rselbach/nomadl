package main

import "time"

type appConfig struct {
	addr            string
	token           string
	namespace       string
	region          string
	logType         string
	tailBytes       int64
	maxLines        int
	refreshInterval time.Duration
}
