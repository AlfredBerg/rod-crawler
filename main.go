package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/AlfredBerg/rod-crawler/internal/js"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/rod/lib/utils"
)

func main() {

	// target := "https://self-signed.badssl.com/"
	target := "http://public-firing-range.appspot.com/urldom/index.html"

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

	router := browser.HijackRequests()

	router.MustAdd("*", func(ctx *rod.Hijack) {
		fmt.Println("Sent request to: ", ctx.Request.URL())
		ctx.ContinueRequest(&proto.FetchContinueRequest{})
	})

	go router.Run()

	// ServeMonitor plays screenshots of each tab. This feature is extremely
	// useful when debugging with headless mode.
	// You can also enable it with flag "-rod=monitor"
	launcher.Open(browser.ServeMonitor(""))

	defer browser.MustClose()

	crawl(browser, target)

	utils.Pause() // pause goroutine
}

func crawl(browser *rod.Browser, target string) {

	// Create a new page
	page := browser.MustPage(target).MustWaitStable()

	// rects := []rect{}
	// result := page.MustEval(js.HIGHLIGHT_CLICKABLE).String()
	// json.Unmarshal([]byte(result), &rects)
	// fmt.Println(rects)

	// fmt.Println(r)
	for i := 0; i < 400; i++ {
		err := page.Timeout(time.Second * 5).WaitStable(time.Second)
		if err != nil {
			log.Printf("wait stable errored out due to: %s\n", err.Error())
		}
		elements := page.MustElementsByJS(js.GET_ELEMENTS)
		if len(elements) == 0 {
			break
		}
		for i := 0; i < 10; i++ {
			sRect := rand.Intn(len(elements))
			e := elements[sRect].Timeout(time.Second * 2)
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

			visible, err := e.Visible()
			if err != nil {
				log.Printf("visible error: %s\n", err.Error())
				continue
			}
			if !visible {
				log.Printf("element is not visible, skipping")
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
	fmt.Println("Nothing more to do")

	// fmt.Println(text)
}
