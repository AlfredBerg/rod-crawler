package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AlfredBerg/rod-crawler/internal/crawl"
	"github.com/AlfredBerg/rod-crawler/internal/outputHandlers/sqlite"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var cfgFile string

type crawlFlags struct {
	targets               string
	concurrency           int
	perCrawltargetTimeout int
	debug                 bool
	logLevel              logLevel

	saveResponses bool

	scope []string
}

var flags crawlFlags

type logLevel string

const (
	debug      logLevel = "debug"
	info       logLevel = "info"
	warn       logLevel = "warn"
	errorLevel logLevel = "error"
)

// String is used both by fmt.Print and by Cobra in help text
func (e *logLevel) String() string {
	return string(*e)
}

// Set must have pointer receiver so it doesn't change the value of a copy
func (e *logLevel) Set(v string) error {
	switch v {
	case "debug", "info", "warn", "error":
		*e = logLevel(v)
		return nil
	default:
		return errors.New(`must be one of "debug", "info", "warn" or "error"`)
	}
}

func (e *logLevel) Type() string {
	return "logLevel"
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	flags = crawlFlags{}

	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.req-diff.yaml)")
	rootCmd.Flags().StringVarP(&flags.targets, "target", "t", "", "A file containing the urls to crawl. If empty stdin is used.")
	rootCmd.Flags().IntVarP(&flags.concurrency, "concurrency", "c", 2, "The number of browsers to be used for crawling at the same time.")
	rootCmd.Flags().IntVar(&flags.perCrawltargetTimeout, "timeout", 60, "The maximum amount of time in seconds to spend on one crawling target.")
	rootCmd.Flags().BoolVarP(&flags.debug, "debug", "d", false, "If specified the browser will not run in headless and auto open devtools.")
	rootCmd.Flags().BoolVarP(&flags.saveResponses, "save-responses", "r", false, "If specified the HTTP responses will be saved when crawling.")
	rootCmd.Flags().Var(&flags.logLevel, "log-level", "Minimum log level to output. Valid values: debug, info, warn, error.")
	rootCmd.Flags().StringSliceVarP(&flags.scope, "scope", "s", nil, "The current browser url of the page being crawled must match one of these or a subdomain of them. "+
		"E.g. example.com matches example.com and all subdomains to example.com. This argument can be specified multiple times")
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag.
		viper.SetConfigFile(cfgFile)
	} else {
		// Find home directory.
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)

		// Search config in home directory with name ".rod-crawler" (without extension).
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName(".rod-crawler")
	}

	viper.AutomaticEnv() // read in environment variables that match

	// If a config file is found, read it in.
	if err := viper.ReadInConfig(); err == nil {
		fmt.Fprintln(os.Stderr, "Using config file:", viper.ConfigFileUsed())
	}
}

var rootCmd = &cobra.Command{
	Use:   "rod-crawler",
	Short: "A simplistic, headless and click-based depth first crawler",

	Run: func(cmd *cobra.Command, args []string) {
		crawler()
	},
}

func crawler() {
	//Setup logger
	c := zap.NewDevelopmentConfig()
	var level zapcore.Level
	switch flags.logLevel {
	case debug:
		level = zapcore.DebugLevel
	case info:
		level = zapcore.InfoLevel
	case warn:
		level = zapcore.WarnLevel
	case errorLevel:
		level = zapcore.ErrorLevel
	default:
		level = zapcore.InfoLevel
	}
	c.Level.SetLevel(level)
	logger := zap.Must(c.Build())
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	// Headless runs the browser on foreground, you can also use flag "-rod=show"
	// Devtools opens the tab in each new tab opened automatically

	bPool := rod.NewBrowserPool(flags.concurrency)
	fCreateBrowser := func() *rod.Browser {
		l := launcher.New().
			Headless(!flags.debug).
			Devtools(flags.debug)
		url := l.MustLaunch()
		go l.Cleanup()

		// Trace shows verbose debug information for each action executed
		// SlowMotion is a debug related function that waits 2 seconds between
		// each action, making it easier to inspect what your code is doing.
		browser := rod.New().
			ControlURL(url).
			//Trace(true).
			//SlowMotion(1 * time.Second).
			MustConnect().
			MustIgnoreCertErrors(true)

		//Don't download files in the browser, e.g. pdf files
		proto.BrowserSetDownloadBehavior{
			Behavior:         proto.BrowserSetDownloadBehaviorBehaviorDeny,
			BrowserContextID: browser.BrowserContextID,
		}.Call(browser)

		//Avoid alerts and close tabs
		go browser.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
			_ = proto.PageHandleJavaScriptDialog{Accept: false, PromptText: ""}.Call(browser)
		},
			func(e *proto.PageWindowOpen) {
				zap.L().Info("new tab opened, trying to close it", zap.String("url", e.URL))
				time.Sleep(time.Millisecond * 500)
				pages, err := browser.Pages()
				if err != nil {
					zap.L().Error("failed getting pages in tab closer", zap.Error(err))
					return
				}
				for _, page := range pages {
					info, err := page.Info()
					if err != nil {
						zap.L().Error("failed getting page info in tab closer", zap.Error(err))
						return
					}
					if info.URL == e.URL {
						err = page.Close()
						if err != nil {
							zap.L().Error("failed closing page in tab closer", zap.Error(err))
							return
						}
					}
				}
			},
		)()

		// go func() {
		// 	for event := range browser.Event() {
		// 		log.Printf("event: %s", event.Method)
		// 	}
		// }()

		return browser
	}

	defer bPool.Cleanup(func(browser *rod.Browser) {
		browser.MustClose()
	})

	outputHandler := sqlite.SqliteOutput{Database: "req.db"}
	outputHandler.Init()
	defer outputHandler.Cleanup()

	// ServeMonitor plays screenshots of each tab. This feature is extremely
	// useful when debugging with headless mode.
	// You can also enable it with flag "-rod=monitor"
	// launcher.Open(browser.ServeMonitor(""))

	targets := make(chan string)
	go func() {
		var sc *bufio.Scanner
		if flags.targets == "" {
			sc = bufio.NewScanner(os.Stdin)
		} else {
			f, err := os.Open(flags.targets)
			if err != nil {
				panic(err)
			}
			sc = bufio.NewScanner(f)
		}
		for sc.Scan() {
			target := strings.ToLower(sc.Text())
			targets <- target
		}
		if sc.Err() != nil {
			panic(sc.Err())
		}
		close(targets)
	}()

	wg := sync.WaitGroup{}
	for i := 0; i < flags.concurrency; i++ {
		wg.Add(1)
		go func() {
			for target := range targets {
				browser := bPool.Get(fCreateBrowser)
				j := crawl.Job{Browser: browser, Target: target, Scope: flags.scope,
					CrawlTimeout: time.Second * time.Duration(flags.perCrawltargetTimeout), OutputHandler: &outputHandler}
				j.Crawl(flags.saveResponses)
				//Cleanup tabs in the browser for the next user
				pages, err := browser.Pages()
				//Keep a blank page to not close the browser
				browser.Page(proto.TargetCreateTarget{URL: ""})
				if err != nil {
					zap.L().Error("failed getting pages to cleanup tabs", zap.Error(err))
					bPool.Put(fCreateBrowser())
					continue
				}
				for _, p := range pages {
					p.Close()
				}

				bPool.Put(browser)
			}
			wg.Done()
		}()
	}
	wg.Wait()

	zap.L().Info("all crawling done")
}
