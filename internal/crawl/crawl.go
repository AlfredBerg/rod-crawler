package crawl

import (
	"crypto/tls"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/AlfredBerg/rod-crawler/internal/js"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
)

func (j *Job) Crawl(saveResponses bool) {
	j.clickedElements = make(map[string]int)

	// Create a new empty page so we can setup request hijacks
	page := j.Browser.Timeout(j.CrawlTimeout).MustPage()
	defer page.Close()

	//Set InsecureSkipVerify as we want to be able to crawl pages with bad certificates
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	client := &http.Client{Transport: tr}
	router := page.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		req, err := httputil.DumpRequest(ctx.Request.Req(), true)
		if err != nil {
			log.Println("failed capturing request with error: ", err)
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		info, err := page.Info()
		if err != nil {
			log.Println("failed getting page info with error: ", err)
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}

		transactionUuid := uuid.New().String()

		j.OutputHandler.HandleRequest(transactionUuid, info.URL, ctx.Request.Req().Method, ctx.Request.Body(), ctx.Request.URL().String(),
			ctx.Request.URL().Path, string(req), ctx.Request.URL().Hostname(), ctx.Request.Req().Header)

		if !saveResponses {
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}

		err = ctx.LoadResponse(client, true)
		if err != nil {
			log.Println("failed loading responses with error: ", err)
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}

		j.OutputHandler.HandleResponse(transactionUuid, ctx.Response.Body(), ctx.Response.Payload().ResponsePhrase, ctx.Response.Payload().ResponseCode, ctx.Response.Headers())

	})
	go router.Run()

	//Keep focus on tab
	go func() {
		t := time.NewTicker(time.Second * 2)

		for range t.C {
			_, err := page.Activate()
			if err != nil {
				log.Printf("failed focusing tab, err %s", err)
				return
			}
		}
	}()

	err := page.Timeout(time.Second * 5).Navigate(j.Target)
	if err != nil {
		log.Printf("could not navigate to the initial page %s, crawling ended early", j.Target)
		return
	}

	for i := 0; i < 400; i++ {
		//Is the context canceled?
		if page.GetContext().Err() != nil {
			break
		}

		info, err := page.Info()
		if err != nil {
			log.Printf("page info errored out due to: %s\n", err.Error())
		}

		currentUrl, err := url.Parse(info.URL)
		if err != nil {
			log.Printf("could not parse url %s: %s\n", info.URL, err.Error())
		}

		//Are we in scope?
		if len(j.Scope) != 0 {
			inScope := false
			for _, s := range j.Scope {
				cHost := currentUrl.Hostname()
				if cHost == s {
					inScope = true
					break
				}
				if strings.HasSuffix(cHost, "."+s) {
					inScope = true
					break
				}
			}
			if !inScope {
				log.Printf("crawler went out of scope, stopping crawl: %s", currentUrl)
				break
			}
		}

		err = page.Timeout(time.Second * 5).WaitStable(time.Second)
		if err != nil {
			log.Printf("wait stable errored out due to: %s\n", err.Error())
		}

		//TOOD: This should be some seperate table in the db, not shoehorned into the request table
		paramUrlRes, err := page.Eval(js.GET_POTENTIAL_PARAMS)
		if err != nil {
			log.Printf("error getting parameters: %s", err.Error())
		} else {
			paramUrl := paramUrlRes.Value.Str()
			if paramUrl != "" {
				url, err := url.Parse(paramUrl)
				if err != nil {
					log.Printf("failed parsing parameter extraction url %s: %s", paramUrl, err.Error())
				} else {
					j.OutputHandler.HandleRequest("", info.URL, "GET", "", paramUrl, url.Path, "", url.Hostname(), nil)
				}
			}
		}

		elements, err := page.ElementsByJS(rod.Eval(js.GET_ELEMENTS))
		if err != nil {
			log.Printf("get elements errored out due to: %s", err.Error())
			continue
		}
		elements = filterNonClickedElements(elements, j.clickedElements)
		if len(elements) == 0 {
			break
		}

		for i := 0; i < 100; i++ {
			//Is the context canceled?
			if page.GetContext().Err() != nil {
				break
			}

			interactUrl, err := url.Parse(info.URL)
			if err != nil {
				log.Printf("could not parse interact url %s: %s\n", info.URL, err.Error())
			}
			// The url has changed, we should run the js to get new clickable elements again and check that we are still in scope
			if currentUrl.String() != interactUrl.String() {
				break
			}

			sRect := rand.Intn(len(elements))
			e := elements[sRect].Timeout(time.Second * 1)
			err = e.ScrollIntoView()
			if err != nil {
				log.Printf("scroll error: %s\n", err.Error())
				continue
			}

			xp, err := e.GetXPath(false)
			if err != nil {
				log.Printf("xpath error: %s\n", err.Error())
				continue
			}

			if j.clickedElements[xp] != 0 {
				fmt.Println("xpath element has already been clicked: ", xp)
				continue
			}

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
			fmt.Println("clicked xpath: ", xp)
			j.clickedElements[xp] += 1
			break
		}
	}
	log.Printf("crawling done for %s", j.Target)
}

func filterNonClickedElements(elements rod.Elements, clickedElements map[string]int) rod.Elements {
	notClickedElements := rod.Elements{}

	for _, e := range elements {
		xp, err := e.GetXPath(false)
		if err != nil {
			log.Printf("failed getting xpath: %s", err.Error())
			continue
		}
		if clickedElements[xp] == 0 {
			notClickedElements = append(notClickedElements, e)
		}
	}

	return notClickedElements
}
