package crawl

import (
	"crypto/tls"
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
	"go.uber.org/zap"
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
			zap.L().Error("failed capturing request with error", zap.Error(err))
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		info, err := page.Info()
		if err != nil {
			zap.L().Error("failed getting page info with error", zap.Error(err))
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
			zap.L().Error("failed loading responses with error", zap.Error(err))
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
				zap.L().Error("failed focusing tab,", zap.Error(err))
				return
			}
		}
	}()

	err := page.Timeout(time.Second * 5).Navigate(j.Target)
	if err != nil {
		zap.L().Error("could not navigate to the initial page, crawling ended early", zap.String("target", j.Target))
		return
	}

	for i := 0; i < 400; i++ {
		//Is the context canceled?
		if page.GetContext().Err() != nil {
			break
		}

		info, err := page.Info()
		if err != nil {
			zap.L().Error("page info errored out due to", zap.Error(err))
		}

		currentUrl, err := url.Parse(info.URL)
		if err != nil {
			zap.L().Error("could not parse url", zap.Error(err), zap.String("url", info.URL))
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
				zap.L().Info("crawler went out of scope, stopping crawl", zap.String("url", currentUrl.String()))
				break
			}
		}

		err = page.Timeout(time.Second * 5).WaitStable(time.Second)
		if err != nil {
			zap.L().Error("wait stable errored out due to", zap.Error(err))
		}

		//TOOD: This should be some seperate table in the db, not shoehorned into the request table
		paramUrlRes, err := page.Eval(js.GET_POTENTIAL_PARAMS)
		if err != nil {
			zap.L().Error("error getting parameters", zap.Error(err))
		} else {
			paramUrl := paramUrlRes.Value.Str()
			if paramUrl != "" {
				url, err := url.Parse(paramUrl)
				if err != nil {
					zap.L().Error("failed parsing parameter extraction url", zap.Error(err), zap.String("url", paramUrl))
				} else {
					j.OutputHandler.HandleRequest("", info.URL, "GET", "", paramUrl, url.Path, "", url.Hostname(), nil)
				}
			}
		}

		elements, err := page.ElementsByJS(rod.Eval(js.GET_ELEMENTS))
		if err != nil {
			zap.L().Error("get elements errored out due to", zap.Error(err))
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
				zap.L().Error("could not parse interact url", zap.Error(err), zap.String("url", info.URL))
			}
			// The url has changed, we should run the js to get new clickable elements again and check that we are still in scope
			if currentUrl.String() != interactUrl.String() {
				break
			}

			sRect := rand.Intn(len(elements))
			e := elements[sRect].Timeout(time.Second * 1)
			err = e.ScrollIntoView()
			if err != nil {
				zap.L().Error("scroll error", zap.Error(err))
				continue
			}

			xp, err := e.GetXPath(false)
			if err != nil {
				zap.L().Error("xapth error", zap.Error(err))
				continue
			}

			if j.clickedElements[xp] != 0 {
				zap.L().Debug("xpath element has already been clicked", zap.String("xpath", xp))
				continue
			}

			//Is the element actually on top and can be clicked?
			jsEvalRes, err := page.Eval(js.IS_TOP_VISIBLE, xp)
			if err != nil {
				zap.L().Error("visible js error", zap.Error(err))
				continue
			}
			isVisible := jsEvalRes.Value

			zap.L().Debug("visibility of xpath", zap.Bool("isVisible", isVisible.Bool()), zap.String("xpath", xp))
			if !isVisible.Bool() {
				continue
			}

			err = e.Click(proto.InputMouseButtonLeft, 1)
			if err != nil {
				zap.L().Error("cick failed", zap.Error(err))
				continue
			}
			zap.L().Info("clicked", zap.String("xpath", xp))
			j.clickedElements[xp] += 1
			break
		}
	}
	zap.L().Info("crawling done for", zap.String("target", j.Target))
}

func filterNonClickedElements(elements rod.Elements, clickedElements map[string]int) rod.Elements {
	notClickedElements := rod.Elements{}

	for _, e := range elements {
		xp, err := e.GetXPath(false)
		if err != nil {
			zap.L().Error("failed getting xpath", zap.Error(err))
			continue
		}
		if clickedElements[xp] == 0 {
			notClickedElements = append(notClickedElements, e)
		}
	}

	return notClickedElements
}
