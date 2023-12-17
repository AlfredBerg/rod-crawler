package cmd

import (
	"bufio"
	"fmt"
	"log"
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
)

var cfgFile string

type crawlFlags struct {
	targets               string
	concurrency           int
	perCrawltargetTimeout int

	scope []string
}

var flags crawlFlags

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
	// Headless runs the browser on foreground, you can also use flag "-rod=show"
	// Devtools opens the tab in each new tab opened automatically
	l := launcher.New().
		Headless(false).
		Devtools(true)

	defer l.Cleanup()

	url := l.MustLaunch()

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

	outputHandler := sqlite.SqliteOutput{Database: "req.db"}
	outputHandler.Init()
	defer outputHandler.Cleanup()

	// ServeMonitor plays screenshots of each tab. This feature is extremely
	// useful when debugging with headless mode.
	// You can also enable it with flag "-rod=monitor"
	launcher.Open(browser.ServeMonitor(""))

	defer browser.MustClose()

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
				j := crawl.Job{Browser: browser, Target: target, Scope: flags.scope,
					CrawlTimeout: time.Second * time.Duration(flags.perCrawltargetTimeout), OutputHandler: &outputHandler}
				j.Crawl()
			}
			wg.Done()
		}()
	}
	wg.Wait()

	log.Printf("all crawling done")
}
