package main

import (
	"bufio"
	"fmt"
	"log"
	"math/rand"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AlfredBerg/rod-crawler/internal/js"
	"github.com/AlfredBerg/rod-crawler/internal/outputHandlers/sqlite"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func main() {

	// target := "https://self-signed.badssl.com/"
	inFile := "targets.txt"
	concurrency := 2
	perCrawltargetTimeout := time.Second * 60

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
		Trace(true).
		// SlowMotion(1 * time.Second).
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
		if inFile == "" {
			sc = bufio.NewScanner(os.Stdin)
		} else {
			f, err := os.Open(inFile)
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
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			for target := range targets {
				crawl(browser, target, perCrawltargetTimeout, &outputHandler)
			}
			wg.Done()
		}()
	}

	wg.Wait()

	log.Printf("all crawling done")
}

func crawl(browser *rod.Browser, target string, crawlTimeout time.Duration, outputHandler *sqlite.SqliteOutput) {
	// Create a new empty page so we can setup request hijacks
	page := browser.Timeout(crawlTimeout).MustPage()
	defer page.Close()

	router := page.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		defer ctx.ContinueRequest(&proto.FetchContinueRequest{})
		req, err := httputil.DumpRequest(ctx.Request.Req(), true)
		if err != nil {
			log.Println("failed capturing request with error: ", err)
			return
		}
		info, err := page.Info()
		if err != nil {
			log.Println("failed getting page info with error: ", err)
			return
		}

		outputHandler.HandleRequest(info.URL, ctx.Request.Req().Method, ctx.Request.Body(), ctx.Request.URL().String(),
			ctx.Request.URL().Path, string(req), ctx.Request.URL().Hostname(), ctx.Request.Req().Header)
	})
	go router.Run()

	err := page.Timeout(time.Second * 5).Navigate(target)
	if err != nil {
		log.Printf("could not navigate to the initial page %s, crawling ended early", target)
		return
	}

	for i := 0; i < 400; i++ {
		//Is the context canceled?
		if page.GetContext().Err() != nil {
			break
		}

		err := page.Timeout(time.Second * 5).WaitStable(time.Second)
		if err != nil {
			log.Printf("wait stable errored out due to: %s\n", err.Error())
		}
		elements, err := page.ElementsByJS(rod.Eval(js.GET_ELEMENTS))
		if err != nil {
			log.Printf("get elements errored out due to: %s\n", err.Error())
		}
		if len(elements) == 0 {
			break
		}

		for i := 0; i < 100; i++ {
			sRect := rand.Intn(len(elements))
			e := elements[sRect].Timeout(time.Second * 1)
			err := e.ScrollIntoView()
			if err != nil {
				log.Printf("scroll error: %s\n", err.Error())
				continue
			}

			xp, err := e.GetXPath(false)

			if err != nil {
				log.Printf("xpath error: %s\n", err.Error())
				continue
			}
			fmt.Println("Xpath: ", xp)

			//Is the element actually on top and can be clicked?
			jsEvalRes, err := page.Eval(js.IS_TOP_VISIBLE, xp)

			if err != nil {
				log.Printf("visible js error: %s\n", err.Error())
				continue
			}
			isVisible := jsEvalRes.Value
			log.Printf("visible: %t", isVisible.Bool())
			if !isVisible.Bool() {
				continue
			}

			err = e.Click(proto.InputMouseButtonLeft, 1)
			if err != nil {
				log.Printf("click error: %s\n", err.Error())
				continue
			}
			break
		}
	}
	log.Printf("crawling done for %s", target)
}
