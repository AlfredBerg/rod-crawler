package crawl

import (
	"time"

	"github.com/AlfredBerg/rod-crawler/internal/outputHandlers/sqlite"
	"github.com/go-rod/rod"
)

type Job struct {
	Browser       *rod.Browser
	Target        string
	CrawlTimeout  time.Duration
	OutputHandler *sqlite.SqliteOutput

	//The current browser url of the page being crawled must match one of these or a subdomain of them
	Scope []string
}
